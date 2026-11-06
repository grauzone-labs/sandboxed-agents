package manager_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestCodexProxyCleanupFailureRestoresPreviousFiles proves failed cleanup after publication restores both prior files and modes through RunWithEffects.
func TestCodexProxyCleanupFailureRestoresPreviousFiles(t *testing.T) {
	for _, removeWorkflow := range []bool{false, true} {
		for _, phase := range []string{"first previous file", "second previous file", "recovery directory"} {
			t.Run(fmt.Sprintf("%s/remove=%t", phase, removeWorkflow), func(t *testing.T) {
				e, dir := codexPublicationEffects(t)
				args := []string{"login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen"}
				if removeWorkflow {
					runAgents(t, e, 0, args...)
					args = []string{"login", "codex", "proxy", "--remove"}
					e.Stdin = forbiddenProxyInput{t}
				}
				originals := map[string][]byte{}
				modes := map[string]os.FileMode{"config.toml": 0640, "auth.json": 0644}
				for name, mode := range modes {
					path := filepath.Join(dir, name)
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					originals[name] = data
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
				}
				removed, injected := 0, false
				e.RemoveFile = func(path string) error {
					previous := strings.HasSuffix(path, ".prev")
					if !injected && (phase == "first previous file" && previous || phase == "second previous file" && previous && removed == 1 || phase == "recovery directory" && filepath.Base(path) == ".sandboxed-agents-proxy") {
						auth, err := os.ReadFile(filepath.Join(dir, "auth.json"))
						if removeWorkflow && !os.IsNotExist(err) || !removeWorkflow && (err != nil || string(auth) != `{"OPENAI_API_KEY":"proxy-key"}`) {
							t.Fatal("cleanup failure did not follow complete publication")
						}
						injected = true
						return fmt.Errorf("proxy-key: injected cleanup failure")
					}
					err := os.Remove(path)
					if err == nil && previous {
						removed++
					}
					return err
				}
				out := runAgents(t, e, 1, args...)
				if !injected || !strings.Contains(out, "previous configuration and login restored") || strings.Contains(out, "proxy-key") {
					t.Fatalf("cleanup did not report restoration without secrets: %s", out)
				}
				for name, want := range originals {
					path := filepath.Join(dir, name)
					got, err := os.ReadFile(path)
					if err != nil || string(got) != string(want) {
						t.Fatalf("%s was not restored: %v", name, err)
					}
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != modes[name] {
						t.Fatalf("%s mode was not restored: %v", name, err)
					}
				}
				if _, err := os.Lstat(filepath.Join(dir, ".sandboxed-agents-proxy")); !os.IsNotExist(err) {
					t.Fatalf("successful restoration left recovery state: %v", err)
				}
			})
		}
	}
}

// TestCodexProxyInterruptedCleanupRetainsRecovery proves partial cleanup leaves a matching new pair and blocks managed runs through RunWithEffects.
func TestCodexProxyInterruptedCleanupRetainsRecovery(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	removed := ""
	e.RemoveFile = func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		if strings.HasSuffix(path, ".prev") {
			removed = path
			panic("interrupted cleanup")
		}
		return nil
	}
	func() {
		defer func() { _ = recover() }()
		runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	}()
	if removed == "" {
		t.Fatal("cleanup interruption was not reached")
	}
	if _, err := os.Lstat(removed); !os.IsNotExist(err) {
		t.Fatalf("interruption did not follow removal: %v", err)
	}
	auth, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil || string(auth) != `{"OPENAI_API_KEY":"proxy-key"}` {
		t.Fatal("published login changed during cleanup")
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil || !strings.Contains(string(config), "https://proxy.example/v1") {
		t.Fatal("published login lacks matching destination")
	}
	e.Start = func(manager.Process) error { t.Fatal("Codex started before recovery"); return nil }
	if out := runAgents(t, e, 1, "run", "codex"); !strings.Contains(out, "requires recovery") {
		t.Fatal(out)
	}
}

// TestCodexProxyCleanupRestorationPreservesConflictingPreviousFile proves reconstruction never overwrites a raced recovery file through RunWithEffects.
func TestCodexProxyCleanupRestorationPreservesConflictingPreviousFile(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	removed := ""
	conflict := []byte("later recovery-file edit")
	e.RemoveFile = func(path string) error {
		if strings.HasSuffix(path, ".prev") {
			if removed != "" {
				if err := os.WriteFile(removed, conflict, 0600); err != nil {
					t.Fatal(err)
				}
				return fmt.Errorf("injected cleanup failure")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			removed = path
			return nil
		}
		return os.Remove(path)
	}
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, "requires recovery") || strings.Contains(out, "proxy-key") {
		t.Fatal(out)
	}
	got, err := os.ReadFile(removed)
	if err != nil || string(got) != string(conflict) {
		t.Fatalf("conflicting previous file was overwritten: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".sandboxed-agents-proxy")); err != nil {
		t.Fatalf("recovery marker lost: %v", err)
	}
}
