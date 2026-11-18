package manager_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestOpenCodeNonregularMetadataRefusesPromptly proves FIFO and directory metadata refuse through RunWithEffects in a bounded child process while preserving metadata and home state.
func TestOpenCodeNonregularMetadataRefusesPromptly(t *testing.T) {
	const childHomeVariable = "SANDBOXED_AGENTS_TEST_OPENCODE_NONREGULAR_HOME"
	if home := os.Getenv(childHomeVariable); home != "" {
		version, installs := "2.3.0", 0
		fmt.Fprintln(os.Stdout, "manager entered installation checks")
		output := runAgents(t, agentEffects(t, home, &version, &installs), 1, "enable", "opencode", "--force")
		if !strings.Contains(output, "package metadata is not a regular file") || installs != 0 {
			t.Fatalf("nonregular metadata refusal=%s installs=%d", output, installs)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"opencode-ai", "@opencode/cli"} {
		for _, kind := range []string{"fifo", "directory"} {
			t.Run(pkg+"/"+kind, func(t *testing.T) {
				home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
				if pkg == "@opencode/cli" {
					runAgents(t, agentEffects(t, home, &version, &installs), 0, "enable", "opencode", "--force")
				}
				command := filepath.Join(home, ".local", "bin", "opencode")
				target, err := os.Readlink(command)
				if err != nil {
					t.Fatal(err)
				}
				metadata := filepath.Join(filepath.Dir(filepath.Dir(target)), "lib", "node_modules", pkg, "package.json")
				if err := os.Remove(command); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(metadata); err != nil {
					t.Fatal(err)
				}
				if kind == "fifo" {
					if err := unix.Mkfifo(metadata, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(metadata, 0700); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(metadata)
				if err != nil {
					t.Fatal(err)
				}
				unchanged := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				child := exec.CommandContext(ctx, executable, "-test.run=^TestOpenCodeNonregularMetadataRefusesPromptly$", "-test.timeout=10s")
				child.Env = append(os.Environ(), childHomeVariable+"="+home)
				output, runError := child.CombinedOutput()
				timedOut := ctx.Err() != nil
				cancel()
				if timedOut {
					t.Errorf("manager blocked on %s metadata; child killed and reaped after 5s: %s", kind, output)
				} else if runError != nil {
					t.Errorf("manager did not reject %s metadata: %v: %s", kind, runError, output)
				}
				if got, err := os.Lstat(metadata); err != nil || !os.SameFile(info, got) || got.Mode() != info.Mode() {
					t.Fatalf("nonregular metadata changed: %v", err)
				}
				if _, err := os.Lstat(command); !os.IsNotExist(err) {
					t.Fatalf("refusal created a command: %v", err)
				}
				unchanged()
			})
		}
	}
}
