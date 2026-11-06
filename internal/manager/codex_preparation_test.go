package manager_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestCodexProxyPreparationFailurePreservesFiles proves a failed recovery-directory creation reports preparation failure without changing active files through RunWithEffects.
func TestCodexProxyPreparationFailurePreservesFiles(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EROFS} {
		t.Run(cause.Error(), func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			originals := map[string][]byte{}
			for _, name := range []string{"config.toml", "auth.json"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				originals[name] = data
			}
			attempted := false
			e.Mkdir = func(string, os.FileMode) error {
				attempted = true
				return &os.PathError{Op: "mkdir", Path: "private-key-path", Err: cause}
			}
			out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
			if !attempted || !strings.Contains(out, "cannot prepare Codex publication; previous configuration and login unchanged") || strings.Contains(out, "requires recovery") || strings.Contains(out, "private-key") {
				t.Fatalf("missing secret-free preparation refusal: %s", out)
			}
			for name, want := range originals {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != string(want) {
					t.Fatalf("%s changed during preparation: %v", name, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, ".sandboxed-agents-proxy")); !os.IsNotExist(err) {
				t.Fatalf("preparation failure left a recovery marker: %v", err)
			}
		})
	}
}

// TestCodexRecoveryInspectionFailureRefusesOperations proves an uninspectable marker path blocks managed runs and logins without prescribing quarantine through RunWithEffects.
func TestCodexRecoveryInspectionFailureRefusesOperations(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	if err := os.Rename(dir, dir+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("private-key-path"), 0600); err != nil {
		t.Fatal(err)
	}
	e.Start = func(manager.Process) error { t.Fatal("started with unknown recovery state"); return nil }
	e.Stdin = forbiddenProxyInput{t}
	for _, args := range [][]string{{"run", "codex"}, {"login", "codex", "browser"}, {"login", "codex", "proxy", "--remove"}} {
		out := runAgents(t, e, 1, args...)
		if !strings.Contains(out, "cannot inspect Codex publication recovery state; operation refused") || strings.Contains(out, "requires recovery") || strings.Contains(out, "private-key") {
			t.Fatalf("missing secret-free inspection refusal: %s", out)
		}
	}
	if got, err := os.ReadFile(dir); err != nil || string(got) != "private-key-path" {
		t.Fatal("uninspectable path was changed")
	}
}

// TestCodexProxyRacedRecoveryDirectoryRequiresRecovery proves a marker created between inspection and creation still requires recovery through RunWithEffects.
func TestCodexProxyRacedRecoveryDirectoryRequiresRecovery(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	attempted := false
	e.Mkdir = func(path string, mode os.FileMode) error {
		attempted = true
		if err := os.Mkdir(path, mode); err != nil {
			t.Fatal(err)
		}
		return os.Mkdir(path, mode)
	}
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !attempted || !strings.Contains(out, "Codex publication requires recovery") {
		t.Fatalf("existing marker did not require recovery: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".sandboxed-agents-proxy")); err != nil {
		t.Fatalf("raced marker was removed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "auth.json")); err != nil || string(got) != `{"OPENAI_API_KEY":"old-key"}` {
		t.Fatal("raced marker changed active login")
	}
}
