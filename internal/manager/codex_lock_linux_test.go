package manager_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
	"golang.org/x/sys/unix"
)

// TestCodexConfigurationLockRejectsUnsafeTargets proves managed runs and proxy writes refuse symlink and nonregular lock targets before process start or key input through RunWithEffects.
func TestCodexConfigurationLockRejectsUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling symlink", "fifo", "directory"} {
		t.Run(kind, func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			root := filepath.Join(filepath.Dir(dir), ".local", "share", "sandboxed-agents")
			lockPath := filepath.Join(root, "codex.lock")
			target := filepath.Join(root, "private-key-target")
			switch kind {
			case "symlink", "dangling symlink":
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("preserved target"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, lockPath); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(lockPath, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(lockPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			e.Start = func(manager.Process) error { starts++; return nil }
			e.Stdin = forbiddenProxyInput{t}
			for _, args := range [][]string{{"run", "codex"}, {"login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen"}} {
				out := runAgents(t, e, 1, args...)
				if starts != 0 || !strings.Contains(out, "Codex configuration lock") || strings.Contains(out, "private-key") {
					t.Fatalf("unsafe lock did not refuse before starting without secrets: %s", out)
				}
			}
			after, err := os.Lstat(lockPath)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("unsafe lock target was replaced or removed")
			}
			if kind == "symlink" {
				if got, err := os.ReadFile(target); err != nil || string(got) != "preserved target" {
					t.Fatal("symlink target was changed")
				}
			} else if kind == "dangling symlink" {
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatal("dangling symlink target was created")
				}
			}
		})
	}
}
