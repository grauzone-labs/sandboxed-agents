package manager_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestOpenCodeProxyMergesSettings proves native provider configuration and private permissions through RunWithEffects.
func TestOpenCodeProxyMergesSettings(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "opencode")
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"model":"openai/old","provider":{"openai":{"options":{"apiKey":"keep"}}},"theme":"system"}`), 0644); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("secret")
	out := runAgents(t, e, 0, "login", "opencode", "proxy", "--url", "https://proxy.example/prefix", "--model", "chosen")
	if !strings.Contains(out, "not checked") {
		t.Fatal(out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	providers := got["provider"].(map[string]any)
	p := providers["cliproxyapi"].(map[string]any)
	options := p["options"].(map[string]any)
	if got["model"] != "cliproxyapi/chosen" || got["theme"] != "system" || providers["openai"] == nil || p["npm"] != "@ai-sdk/openai-compatible" || options["baseURL"] != "https://proxy.example/prefix/v1" || options["apiKey"] != "secret" || p["models"].(map[string]any)["chosen"] == nil {
		t.Fatal("incorrect merged config")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("incorrect permissions", err)
	}
}

// TestOpenCodeProxyFailedPublicationPreservesBytes proves a staging failure leaves settings and permissions byte-for-byte intact through RunWithEffects.
func TestOpenCodeProxyFailedPublicationPreservesBytes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission checks")
	}
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "opencode")
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "opencode.json")
	original := []byte("{ \"model\": \"original\" }\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	e.Stdin = strings.NewReader("secret")
	runAgents(t, e, 1, "login", "opencode", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, original) {
		t.Fatalf("failure altered original: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatal("failure changed mode")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file left: %v %v", entries, err)
	}
}

// TestOpenCodeProxyRefusesConflicts proves invalid JSON and unowned providers stay byte-identical at RunWithEffects.
func TestOpenCodeProxyRefusesConflicts(t *testing.T) {
	for _, original := range []string{`{"provider":{"cliproxyapi":{}}}`, `{"x": /*comment*/ 1}`, `null`, `{"provider":[]}`, `{`} {
		t.Run(original, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "opencode")
			path := filepath.Join(home, ".config", "opencode", "opencode.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			e.Stdin = strings.NewReader("secret")
			out := runAgents(t, e, 1, "login", "opencode", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			if !strings.Contains(out, path) {
				t.Fatal(out)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != original {
				t.Fatal("changed refused configuration", err)
			}
		})
	}
}

// TestOpenCodeProxyReplacesAndRemoves proves repeated setup and conditional model restoration through RunWithEffects.
func TestOpenCodeProxyReplacesAndRemoves(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprint(edited), func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "opencode")
			path := filepath.Join(home, ".config", "opencode", "opencode.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"model":"openai/original","provider":{"anthropic":{"options":{"apiKey":"original-key"}}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"first", "second"} {
				e.Stdin = strings.NewReader("secret-" + model)
				runAgents(t, e, 0, "login", "opencode", "proxy", "--url", "https://"+model+".example", "--model", model)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(b, []byte("https://second.example/v1")) || !bytes.Contains(b, []byte("secret-second")) || bytes.Contains(b, []byte("secret-first")) {
				t.Fatal("replacement failed")
			}
			if edited {
				b = bytes.Replace(b, []byte(`"model":"cliproxyapi/second"`), []byte(`"model":"openai/user"`), 1)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.Stdin = forbiddenProxyInput{t}
			runAgents(t, e, 0, "login", "opencode", "proxy", "--remove")
			b, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte("cliproxyapi")) || bytes.Contains(b, []byte("secret")) {
				t.Fatal("managed entries remain")
			}
			if !edited && !bytes.Contains(b, []byte("openai/original")) {
				t.Fatal("prior model not restored")
			}
			if !bytes.Contains(b, []byte("original-key")) {
				t.Fatal("existing provider lost")
			}
			if edited && !bytes.Contains(b, []byte("openai/user")) {
				t.Fatal("edited model lost")
			}
		})
	}
}

// TestOpenCodeProxyRefusesJSONC proves an adjacent JSONC file is named and preserved through RunWithEffects.
func TestOpenCodeProxyRefusesJSONC(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "opencode")
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("secret")
	out := runAgents(t, e, 1, "login", "opencode", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, path) {
		t.Fatal(out)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, "c")); !os.IsNotExist(err) {
		t.Fatal("created JSON alongside JSONC")
	}
}

// TestOpenCodeProxyRequiresModelBeforeInput proves manager validation refuses a missing model before home or key access through RunWithEffects.
func TestOpenCodeProxyRequiresModelBeforeInput(t *testing.T) {
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.Stdin = forbiddenProxyInput{t}
	e.HomeDirectory = func() (string, error) { t.Fatal("unexpected home access"); return "", nil }
	out := runAgents(t, e, 2, "login", "opencode", "proxy", "--url", "https://proxy.example")
	if !strings.Contains(out, "--model") {
		t.Fatal(out)
	}
}

// TestOpenCodeProxyRejectsLinkedSettings proves symlink and hardlink settings cannot redirect reads or writes through RunWithEffects.
func TestOpenCodeProxyRejectsLinkedSettings(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "opencode")
			outside := t.TempDir()
			target := filepath.Join(outside, "opencode.json")
			if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".config", "opencode")
			path := filepath.Join(dir, "opencode.json")
			var err error
			if kind == "directory-symlink" {
				if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, dir)
			} else {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = os.Symlink(target, path)
				} else {
					err = os.Link(target, path)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			e.Stdin = strings.NewReader("secret")
			runAgents(t, e, 1, "login", "opencode", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			b, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != `{}` {
				t.Fatal("linked settings changed")
			}
		})
	}
}
