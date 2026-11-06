package manager_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestClaudeProxyMergesNativeSettings proves the proxy workflow merges private native settings and leaves OAuth untouched through RunWithEffects.
func TestClaudeProxyMergesNativeSettings(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"original","permissions":{"allow":["Read"]},"env":{"OTHER":"keep"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	oauth := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(oauth, []byte("browser-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("proxy-secret\nignored\n")
	out := runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example/prefix/", "--model", "chosen")
	if strings.Contains(out, "proxy-secret") || !strings.Contains(out, "not checked") {
		t.Fatalf("output=%s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	env := settings["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://proxy.example/prefix" || env["ANTHROPIC_AUTH_TOKEN"] != "proxy-secret" || env["OTHER"] != "keep" || settings["model"] != "chosen" || settings["permissions"] == nil {
		t.Fatalf("incorrect merged settings")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode: %v %v", info, err)
	}
	if b, err := os.ReadFile(oauth); err != nil {
		t.Fatal(err)
	} else if string(b) != "browser-credential" {
		t.Fatal("OAuth changed")
	}
	if strings.Count(string(data), "proxy-secret") != 1 {
		t.Fatal("duplicate key copy")
	}
}

// TestClaudeProxyRemovalRestoresOnlyOwnedSettings proves replacement and removal preserve user edits, model history and OAuth through RunWithEffects.
func TestClaudeProxyRemovalRestoresOnlyOwnedSettings(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprint(edited), func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "claude")
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := `{"model":"original","env":{"OTHER":"keep"},"permissions":{"allow":["Read"]}}`
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"first", "second"} {
				e.Stdin = strings.NewReader("secret-" + model)
				runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example", "--model", model)
			}
			if edited {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var editedSettings map[string]json.RawMessage
				if err := json.Unmarshal(b, &editedSettings); err != nil {
					t.Fatal(err)
				}
				editedSettings["model"] = json.RawMessage(`"user-edited"`)
				b, err = json.Marshal(editedSettings)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.Stdin = forbiddenProxyInput{t}
			runAgents(t, e, 0, "login", "claude", "proxy", "--remove")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal(b, &settings); err != nil {
				t.Fatal(err)
			}
			want := "original"
			if edited {
				want = "user-edited"
			}
			if settings["model"] != want {
				t.Fatalf("model=%v want=%s", settings["model"], want)
			}
			env := settings["env"].(map[string]any)
			if len(env) != 1 || env["OTHER"] != "keep" || len(settings) != 3 {
				t.Fatalf("unrelated settings lost or managed entries remain")
			}
			runAgents(t, e, 0, "login", "claude", "proxy", "--remove")
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, after) {
				t.Fatal("repeated removal changed native settings")
			}
		})
	}
}

// TestClaudeProxyRemovalRefusesToDetachEditedKey proves removal preserves the complete settings file when deleting its managed endpoint would detach a user-edited token through RunWithEffects.
func TestClaudeProxyRemovalRefusesToDetachEditedKey(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	e.Stdin = strings.NewReader("managed-secret")
	runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example", "--model", "managed-model")
	path := filepath.Join(home, ".claude", "settings.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(original, []byte("managed-secret"), []byte("user-edited-secret"), 1)
	if bytes.Equal(edited, original) {
		t.Fatal("managed token missing from native settings")
	}
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	e.Stdin = forbiddenProxyInput{t}
	out := runAgents(t, e, 1, "login", "claude", "proxy", "--remove")
	if !strings.Contains(out, "edited") || !strings.Contains(out, "together") || strings.Contains(out, "user-edited-secret") {
		t.Fatalf("unsafe or unclear refusal: %s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, edited) {
		t.Fatal("refusal changed the endpoint, token, model or ownership metadata")
	}
}

// TestClaudeProxyRemovalAfterOtherEnvEdits proves removal clears remaining owned env entries and preserves user-edited pairs through RunWithEffects.
func TestClaudeProxyRemovalAfterOtherEnvEdits(t *testing.T) {
	for _, tc := range []struct{ name, env, wantEnv string }{
		{"edited-endpoint", `{"ANTHROPIC_BASE_URL":"https://edited.example","ANTHROPIC_AUTH_TOKEN":"managed-secret"}`, `{"ANTHROPIC_BASE_URL":"https://edited.example"}`},
		{"deleted-token", `{"ANTHROPIC_BASE_URL":"https://proxy.example"}`, `{}`},
		{"both-edited", `{"ANTHROPIC_BASE_URL":"https://edited.example","ANTHROPIC_AUTH_TOKEN":"edited-secret"}`, `{"ANTHROPIC_BASE_URL":"https://edited.example","ANTHROPIC_AUTH_TOKEN":"edited-secret"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "claude")
			e.Stdin = strings.NewReader("managed-secret")
			runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example")
			path := filepath.Join(home, ".claude", "settings.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]json.RawMessage
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			settings["env"] = json.RawMessage(tc.env)
			data, err = json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			e.Stdin = forbiddenProxyInput{t}
			runAgents(t, e, 0, "login", "claude", "proxy", "--remove")
			data, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(`{"env":`+tc.wantEnv+`}`), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("removal did not preserve edited env values or left ownership metadata")
			}
		})
	}
}

// forbiddenProxyInput fails when a request that must not consume a key reads standard input.
type forbiddenProxyInput struct{ t *testing.T }

// Read reports an unexpected key read at the manager input boundary.
func (r forbiddenProxyInput) Read([]byte) (int, error) {
	r.t.Fatal("unexpected key read")
	return 0, io.EOF
}

// TestClaudeProxyRefusesUnsafeInput proves invalid keys and conflicting or malformed settings leave native bytes unchanged and never expose secrets through RunWithEffects.
func TestClaudeProxyRefusesUnsafeInput(t *testing.T) {
	for _, tc := range []struct{ name, settings, key, cause string }{
		{"eof", `{"model":"keep"}`, "", "key"},
		{"empty", `{}`, "\n", "key"},
		{"whitespace", `{}`, "secret with-space", "whitespace"},
		{"cr", `{}`, "secret\rkey", "control"},
		{"nul", `{}`, "secret\x00", "control"},
		{"api-key", `{"env":{"ANTHROPIC_API_KEY":"private-existing"}}`, "new-secret", "ANTHROPIC_API_KEY"},
		{"helper", `{"apiKeyHelper":"private-existing"}`, "new-secret", "apiKeyHelper"},
		{"malformed", `{"private-existing":`, "new-secret", "malformed"},
		{"bad-env", `{"env":"private-existing"}`, "new-secret", "malformed"},
		{"null", `null`, "new-secret", "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "claude")
			path := filepath.Join(home, ".claude", "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.settings), 0644); err != nil {
				t.Fatal(err)
			}
			e.Stdin = strings.NewReader(tc.key)
			out := runAgents(t, e, 1, "login", "claude", "proxy", "--url", "https://proxy.example")
			if !strings.Contains(out, tc.cause) || strings.Contains(out, "private-existing") || strings.Contains(out, "new-secret") {
				t.Fatalf("unsafe or unclear error %s", out)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.settings {
				t.Fatal("refusal changed native settings")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatal("refusal changed permissions")
			}
			if tc.name == "api-key" || tc.name == "helper" {
				e.Stdin = forbiddenProxyInput{t}
				out = runAgents(t, e, 1, "login", "claude", "proxy", "--remove")
				if !strings.Contains(out, tc.cause) || strings.Contains(out, "private-existing") {
					t.Fatalf("unsafe or unclear removal error %s", out)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(b, after) {
					t.Fatal("conflicting removal changed native settings")
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0644 {
					t.Fatal("conflicting removal changed permissions")
				}
			}
		})
	}
}

// TestClaudeProxyGrammarRefusesBeforeHomeAccess proves malformed proxy options are usage errors before key or home access at RunWithEffects.
func TestClaudeProxyGrammarRefusesBeforeHomeAccess(t *testing.T) {
	for _, args := range [][]string{
		{"proxy"}, {"proxy", "--url", "bad"}, {"proxy", "--url", "https://example/v1"},
		{"proxy", "--remove", "--url", "https://example"}, {"proxy", "--remove", "--model", "x"},
		{"browser", "--url", "https://example"}, {"browser", "--model", "x"},
		{"proxy", "--key", "private-secret"}, {"proxy", "private-secret"},
		{"proxy", "--url", "https://example", "--model"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			e.Stdin = forbiddenProxyInput{t}
			e.HomeDirectory = func() (string, error) { t.Fatal("usage accessed home"); return "", nil }
			out := runAgents(t, e, 2, append([]string{"login", "claude"}, args...)...)
			if strings.Contains(out, "private-secret") {
				t.Fatal("usage leaked secret")
			}
		})
	}
}

// TestClaudeProxyUsesManagerLockAndSurvivesAgentLifecycle proves proxy mutations refuse a held lock and reinstall and disable retain native settings at RunWithEffects.
func TestClaudeProxyUsesManagerLockAndSurvivesAgentLifecycle(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	e.Stdin = strings.NewReader("secret")
	runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	path := filepath.Join(home, ".claude", "settings.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, held, err := lockFor(t, home)
	if err != nil || !held {
		t.Fatalf("lock: %v %v", held, err)
	}
	e.Stdin = forbiddenProxyInput{t}
	runAgents(t, e, 1, "login", "claude", "proxy", "--remove")
	unlock()
	version = "2.0.0"
	runAgents(t, e, 0, "update", "claude")
	runAgents(t, e, 0, "disable", "claude")
	runAgents(t, e, 0, "enable", "claude")
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
		if strings.Contains(string(b), "secret") && path != filepath.Join(home, ".claude", "settings.json") {
			t.Errorf("extra plaintext key in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeProxyRestoresEquivalentModelJSON proves harmless string escaping does not lose the previous model through RunWithEffects.
func TestClaudeProxyRestoresEquivalentModelJSON(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	e.Stdin = strings.NewReader("secret")
	runAgents(t, e, 0, "login", "claude", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	path := filepath.Join(home, ".claude", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	settings["model"] = json.RawMessage(`"\u0063hosen"`)
	settings["env"] = json.RawMessage(`{"ANTHROPIC_BASE_URL":"https:\/\/proxy.example","ANTHROPIC_AUTH_TOKEN":"\u0073ecret"}`)
	b, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	runAgents(t, e, 0, "login", "claude", "proxy", "--remove")
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	settings = nil
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	if _, exists := settings["model"]; exists {
		t.Fatal("equivalent model was not restored to absent")
	}
	if strings.Contains(string(b), "ANTHROPIC_") {
		t.Fatal("equivalent env values were not removed")
	}
}

// TestClaudeProxyRejectsLinkedSettings proves symlink and hardlink settings cannot redirect reads or writes through RunWithEffects.
func TestClaudeProxyRejectsLinkedSettings(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "claude")
			outside := t.TempDir()
			target := filepath.Join(outside, "settings.json")
			if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".claude")
			path := filepath.Join(dir, "settings.json")
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
			runAgents(t, e, 1, "login", "claude", "proxy", "--url", "https://proxy.example")
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

// TestClaudeProxyFailedPublicationPreservesBytes proves a staging failure leaves settings and permissions byte-for-byte intact through RunWithEffects.
func TestClaudeProxyFailedPublicationPreservesBytes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission checks")
	}
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
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
	runAgents(t, e, 1, "login", "claude", "proxy", "--url", "https://proxy.example")
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

// TestClaudeProxyUnownedRemovalIsBytePreserving proves removal without workflow metadata leaves native formatting and credentials unchanged through RunWithEffects.
func TestClaudeProxyUnownedRemovalIsBytePreserving(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	original := []byte("{ \"env\": {\"ANTHROPIC_AUTH_TOKEN\": \"user-owned\"} }\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	e.Stdin = forbiddenProxyInput{t}
	runAgents(t, e, 0, "login", "claude", "proxy", "--remove")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, original) {
		t.Fatal("unowned removal rewrote settings")
	}
}
