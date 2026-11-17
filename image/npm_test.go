package image_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// npmFixtureVersion is the latest version advertised and packed by the isolated registry.
const npmFixtureVersion = "11.999.0"

// TestBaseImageInstallsLatestNPM proves the embedded build step installs the registry's latest npm and rejects an incompatible release through a real shell and npm with an isolated registry and prefix.
func TestBaseImageInstallsLatestNPM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the image build step runs on Linux")
	}
	for _, program := range []string{"npm", "node"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Fatalf("image npm build check needs %s: %v", program, err)
		}
	}
	recipe, err := fs.ReadFile(image.Context(), "base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	var step string
	for _, instruction := range strings.Split(strings.ReplaceAll(string(recipe), "\\\n", ""), "\n") {
		if strings.HasPrefix(instruction, "RUN ") && strings.Contains(instruction, "npm@latest") {
			step = strings.TrimPrefix(instruction, "RUN ")
			break
		}
	}
	if step == "" {
		t.Fatal("base image has no build step installing npm@latest")
	}
	for _, test := range []struct {
		name, engine string
		compatible   bool
	}{
		{"compatible latest", ">=0", true},
		{"incompatible latest", ">=999", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := npmFixtureArchive(t, test.engine)
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/npm":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"name":"npm","dist-tags":{"latest":%q},"versions":{%q:{"name":"npm","version":%q,"engines":{"node":%q},"bin":{"npm":"bin/npm-cli.js"},"dist":{"tarball":"http://%s/npm/-/npm-%s.tgz"}}}}`, npmFixtureVersion, npmFixtureVersion, npmFixtureVersion, test.engine, r.Host, npmFixtureVersion)
				case "/npm/-/npm-" + npmFixtureVersion + ".tgz":
					w.Write(archive)
				default:
					http.NotFound(w, r)
				}
			}))
			defer registry.Close()
			root := t.TempDir()
			prefix := filepath.Join(root, "prefix")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-c", step)
			command.WaitDelay = time.Second
			// Give real npm no host configuration, credentials, cache or global prefix.
			command.Env = []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + root,
				"npm_config_registry=" + registry.URL,
				"npm_config_prefix=" + prefix,
				"npm_config_cache=" + filepath.Join(root, "cache"),
				"npm_config_userconfig=" + filepath.Join(root, "user.npmrc"),
				"npm_config_globalconfig=" + filepath.Join(root, "global.npmrc"),
				"npm_config_update_notifier=false", "npm_config_audit=false",
				"npm_config_fund=false", "npm_config_fetch_retries=0",
			}
			output, err := command.CombinedOutput()
			installed := filepath.Join(prefix, "bin", "npm")
			if !test.compatible {
				if err == nil || !bytes.Contains(output, []byte("EBADENGINE")) {
					t.Fatalf("incompatible latest: error=%v, output=%s; want engine refusal", err, output)
				}
				if _, err := os.Stat(installed); !os.IsNotExist(err) {
					t.Fatalf("incompatible npm was installed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("install latest npm: %v\n%s", err, output)
			}
			probe := exec.CommandContext(ctx, installed, "--version")
			probe.Env = command.Env
			if output, err := probe.CombinedOutput(); err != nil || string(output) != "11.999.0\n" {
				t.Fatalf("installed npm --version: %q, %v; want latest 11.999.0", output, err)
			}
		})
	}
}

// npmFixtureArchive packs a dependency-free npm fixture with the requested Node engine constraint for the local registry.
func npmFixtureArchive(t *testing.T, engine string) []byte {
	t.Helper()
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	for name, content := range map[string]string{
		"package/package.json":   fmt.Sprintf(`{"name":"npm","version":%q,"engines":{"node":%q},"bin":{"npm":"bin/npm-cli.js"}}`, npmFixtureVersion, engine),
		"package/bin/npm-cli.js": fmt.Sprintf("#!/usr/bin/env node\nconsole.log(%q)\n", npmFixtureVersion),
	} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
