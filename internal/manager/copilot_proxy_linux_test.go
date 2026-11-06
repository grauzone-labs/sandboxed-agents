package manager_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestCopilotProxyMergesRegistry proves native provider/model merging, private file mode, and credential preservation through RunWithEffects.
func TestCopilotProxyMergesRegistry(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.106", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot")
	dir := filepath.Join(home, ".copilot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "providers.json")
	original := `{"providers":[{"name":"other","type":"openai","apiKey":"other-key"}],"models":[{"id":"keep","provider":"other"}],"extra":{"keep":true}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "config.json")
	if err := os.WriteFile(token, []byte(`{"copilot_tokens":{"github.com":"github-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("proxy-secret\nignored\n")
	out := runAgents(t, e, 0, "login", "copilot", "proxy", "--url", "https://proxy.example/prefix/", "--model", "chosen")
	if strings.Contains(out, "proxy-secret") || !strings.Contains(out, "not checked") || !strings.Contains(out, "--model cliproxyapi/chosen") {
		t.Fatalf("output=%s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Providers []map[string]any
		Models    []map[string]any
		Extra     map[string]bool
	}
	if err = json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.Providers) != 2 || len(registry.Models) != 2 || !registry.Extra["keep"] {
		t.Fatalf("registry=%s", data)
	}
	p := registry.Providers[1]
	if p["name"] != "cliproxyapi" || p["type"] != "openai" || p["baseUrl"] != "https://proxy.example/prefix/v1" || p["apiKey"] != "proxy-secret" || p["wireApi"] != nil {
		t.Fatalf("provider=%v", p)
	}
	if registry.Providers[0]["apiKey"] != "other-key" || registry.Models[0]["id"] != "keep" || registry.Models[1]["id"] != "chosen" || registry.Models[1]["provider"] != "cliproxyapi" {
		t.Fatal("incorrect merge")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
		t.Fatal("registry has wrong owner")
	}
	if strings.Count(string(data), "proxy-secret") != 1 {
		t.Fatal("duplicate key copy")
	}
	b, err := os.ReadFile(token)
	if err != nil || string(b) != `{"copilot_tokens":{"github.com":"github-token"}}` {
		t.Fatal("GitHub credentials changed")
	}
}

// TestCopilotProxyVersionBeforeKey proves an old pinned installation refuses configuration before reading stdin through RunWithEffects.
func TestCopilotProxyVersionBeforeKey(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.105", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot", "--version", version)
	e.Stdin = nil
	out := runAgents(t, e, 1, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, "1.0.106") || !strings.Contains(out, "agents enable") || strings.Contains(out, "key is required") {
		t.Fatalf("wrong refusal: %s", out)
	}
}

// TestCopilotProxyReplacementAndRemoval proves both wire APIs, model retention and idempotent selective removal through RunWithEffects.
func TestCopilotProxyReplacementAndRemoval(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.106", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot")
	for _, model := range []string{"first", "second", "second"} {
		e.Stdin = strings.NewReader("secret-" + model)
		runAgents(t, e, 0, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", model, "--wire-api", "responses")
	}
	path := filepath.Join(home, ".copilot", "providers.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		t.Fatal("invalid JSON")
	}
	var models []map[string]string
	if json.Unmarshal(doc["models"], &models) != nil || len(models) != 2 {
		t.Fatalf("models=%s", doc["models"])
	}
	if !strings.Contains(string(doc["providers"]), `"wireApi":"responses"`) || strings.Contains(string(data), "secret-first") {
		t.Fatal("replacement failed")
	}
	e.Stdin = strings.NewReader("replacement-key")
	runAgents(t, e, 0, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "second", "--wire-api", "completions")
	replacement, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(replacement), "wireApi") || strings.Contains(string(replacement), "secret-second") {
		t.Fatal("completions did not replace responses")
	}
	e.Stdin = nil
	runAgents(t, e, 0, "login", "copilot", "proxy", "--remove")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "cliproxyapi") || strings.Contains(string(after), "secret-") || strings.Contains(string(after), "_sandboxedAgentsProxy") {
		t.Fatalf("removal incomplete: %s", after)
	}
	runAgents(t, e, 0, "login", "copilot", "proxy", "--remove")
	again, err := os.ReadFile(path)
	if err != nil || string(again) != string(after) {
		t.Fatal("removal not idempotent")
	}
}

// TestCopilotProxyRefusesUnownedOrMalformedRegistry proves refusal preserves exact bytes and permissions through RunWithEffects.
func TestCopilotProxyRefusesUnownedOrMalformedRegistry(t *testing.T) {
	for _, original := range []string{
		`{`, `null`, `{"providers":null}`, `{"models":{}}`, `{"models":[null]}`,
		`{"providers":[{"name":"cliproxyapi","apiKey":"user-secret"}]}`,
		`{"models":[{"id":"chosen","provider":"cliproxyapi"}]}`,
		`{"_sandboxedAgentsProxy":{"version":7}}`,
	} {
		t.Run(original, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.106", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "copilot")
			path := filepath.Join(home, ".copilot", "providers.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"--url", "https://proxy.example", "--model", "chosen"}, {"--remove"}} {
				e.Stdin = strings.NewReader("new-secret")
				out := runAgents(t, e, 1, append([]string{"login", "copilot", "proxy"}, args...)...)
				if strings.Contains(out, "user-secret") || strings.Contains(out, "new-secret") {
					t.Fatal("key in error")
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != original {
					t.Fatal("refusal changed bytes")
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatal("refusal changed mode")
				}
			}
		})
	}
}

// TestCopilotProxyRejectsLinkedSettings proves symlink and hardlink settings cannot redirect reads or writes through RunWithEffects.
func TestCopilotProxyRejectsLinkedSettings(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.106", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "copilot")
			outside := t.TempDir()
			target := filepath.Join(outside, "providers.json")
			if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".copilot")
			path := filepath.Join(dir, "providers.json")
			var err error
			if kind == "directory-symlink" {
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
			runAgents(t, e, 1, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
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

// TestCopilotProxyFailedPublicationPreservesBytes proves a staging failure leaves settings and permissions byte-for-byte intact through RunWithEffects.
func TestCopilotProxyFailedPublicationPreservesBytes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission checks")
	}
	home, version, installs := t.TempDir(), "1.0.106", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot")
	dir := filepath.Join(home, ".copilot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "providers.json")
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
	runAgents(t, e, 1, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
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

// TestCopilotProxyPreservesUnrelatedModelsOnRemoval proves selective removal and edited-provider refusal through RunWithEffects.
func TestCopilotProxyPreservesUnrelatedModelsOnRemoval(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.106", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot")
	e.Stdin = strings.NewReader("proxy-secret")
	runAgents(t, e, 0, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	path := filepath.Join(home, ".copilot", "providers.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.ReplaceAll(original, []byte("proxy-secret"), []byte("user-edited-secret"))
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--remove"}, {"--url", "https://proxy.example", "--model", "next"}} {
		e.Stdin = strings.NewReader("replacement-secret")
		runAgents(t, e, 1, append([]string{"login", "copilot", "proxy"}, args...)...)
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, edited) {
			t.Fatal("edited provider overwritten")
		}
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(original, &doc) != nil {
		t.Fatal("bad registry")
	}
	doc["models"] = json.RawMessage(`[{"id":"chosen","provider":"cliproxyapi"},{"id":"user-added","provider":"cliproxyapi"},{"id":"keep","provider":"other","custom":true}]`)
	doc["providers"] = append(append(json.RawMessage{}, doc["providers"][:len(doc["providers"])-1]...), []byte(`,{"name":"other","apiKey":"keep"}]`)...)
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = nil
	runAgents(t, e, 0, "login", "copilot", "proxy", "--remove")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &doc) != nil || string(doc["models"]) != `[{"custom":true,"id":"keep","provider":"other"}]` || string(doc["providers"]) != `[{"apiKey":"keep","name":"other"}]` {
		t.Fatalf("unrelated entries changed: %s", data)
	}
}

// TestCopilotProxyUsesManagerLockAndSurvivesAgentLifecycle proves proxy mutations refuse a held lock and reinstall and disable retain native settings at RunWithEffects.
func TestCopilotProxyUsesManagerLockAndSurvivesAgentLifecycle(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.106", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "copilot")
	e.Stdin = strings.NewReader("secret")
	runAgents(t, e, 0, "login", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	path := filepath.Join(home, ".copilot", "providers.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, held, err := lockFor(t, home)
	if err != nil || !held {
		t.Fatalf("lock: %v %v", held, err)
	}
	e.Stdin = forbiddenProxyInput{t}
	runAgents(t, e, 1, "login", "copilot", "proxy", "--remove")
	unlock()
	version = "2.0.0"
	runAgents(t, e, 0, "update", "copilot")
	runAgents(t, e, 0, "disable", "copilot")
	runAgents(t, e, 0, "enable", "copilot")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("lifecycle changed settings")
	}
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "secret") && path != filepath.Join(home, ".copilot", "providers.json") {
			t.Errorf("extra plaintext key in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
