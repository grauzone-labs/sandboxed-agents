package image_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestBaseRecipeSuppliesRuntimeContents proves the agreed runtime, agent identity, manager and version manifest are installed without baked SSH host keys through the embedded recipe seam.
func TestBaseRecipeSuppliesRuntimeContents(t *testing.T) {
	data, err := fs.ReadFile(image.Context(), "base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(data)
	for _, required := range []string{
		"FROM debian:trixie-slim", "openssh-server", "openssh-client", "git", "gh", "tmux", "curl", "jq", "ripgrep", "less", "unzip", "ca-certificates",
		"groupadd --gid 1000 agent", "useradd --uid 1000 --gid 1000", "rm -f /etc/ssh/ssh_host_*",
		"COPY manager.bin /usr/local/bin/sandboxed-agents-manager", "chmod 0755 /usr/local/bin/sandboxed-agents-manager",
		"/usr/local/share/sandboxed-agents/versions.txt", "dpkg-query", "node --version", "npm --version",
	} {
		if !strings.Contains(recipe, required) {
			t.Errorf("base recipe is missing %q", required)
		}
	}
	for _, toolchain := range []string{"dotnet-sdk", "azure-cli", "playwright install", "build-essential", "npm install -g"} {
		if strings.Contains(recipe, toolchain) {
			t.Errorf("base recipe unexpectedly installs %q", toolchain)
		}
	}
}

// TestSelectedToolchainsLayerOnBase proves each delivered toolchain and a combined set receive a canonical selected recipe layered on the base image through the image build boundary.
func TestSelectedToolchainsLayerOnBase(t *testing.T) {
	for _, set := range [][]string{{"dotnet"}, {"playwright"}, {"azure"}, {"native"}, {"playwright", "dotnet", "azure", "native", "dotnet"}} {
		t.Run(strings.Join(set, ","), func(t *testing.T) {
			hash, err := image.Hash(image.Context())
			if err != nil {
				t.Fatal(err)
			}
			tag, err := image.BuildWithToolchains(context.Background(), nil, set, func(_ context.Context, args ...string) error {
				data, err := os.ReadFile(args[2])
				if err != nil {
					t.Fatal(err)
				}
				recipe := string(data)
				if !strings.HasPrefix(recipe, "ARG BASE_IMAGE=localhost/sandboxed-agents:none-"+hash+"\nFROM ${BASE_IMAGE}\n") {
					t.Fatalf("selected recipe does not layer on base: %s", recipe)
				}
				if filepath.Base(args[5]) == "base" {
					t.Fatal("toolchain context must include the selected fragments")
				}
				for name, install := range map[string]string{"dotnet": "dotnet-sdk-10.0", "playwright": "playwright install --with-deps chromium firefox webkit", "azure": "azure-cli python3-azext-devops", "native": "build-essential cmake ninja-build pkg-config gdb"} {
					selected := false
					for _, entry := range set {
						selected = selected || name == entry
					}
					if got := strings.Count(recipe, install); got != 0 && !selected || selected && got != 1 {
						t.Errorf("%s install count = %d, selected = %t", name, got, selected)
					}
				}
				if !strings.Contains(recipe, "record-versions.sh") {
					t.Fatal("toolchain recipe does not refresh versions manifest")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tag != image.Tag(hash, set) {
				t.Fatalf("tag = %q", tag)
			}
		})
	}
}
