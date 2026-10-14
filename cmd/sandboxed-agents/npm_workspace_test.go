package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestNpmLaunchersAbsentFromPATHAreProtected proves npm launchers and shims absent from PATH are refused as workspace binds before Podman or SSH through the built CLI executable.
func TestNpmLaunchersAbsentFromPATHAreProtected(t *testing.T) {
	for _, kind := range []string{"launcher.cjs", "sandboxed-agents.cmd", "sandboxed-agents.ps1", "launch link", "workspace alias"} {
		t.Run(kind, func(t *testing.T) {
			assertNpmLauncherProtected(t, kind)
		})
	}
}

func assertNpmLauncherProtected(t *testing.T, kind string) {
	t.Helper()
	fakes := testutil.Install(t, fakeExecutable)
	directory := t.TempDir()
	launcher := filepath.Join(directory, kind)
	if err := os.WriteFile(launcher, []byte("launcher fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := directory
	if kind == "launch link" {
		launcher = filepath.Join(t.TempDir(), "sandboxed-agents")
		if err := os.Symlink(filepath.Join(directory, kind), launcher); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		workspace = filepath.Dir(launcher)
	}
	if kind == "workspace alias" {
		workspace = filepath.Join(t.TempDir(), "workspace-alias")
		if err := os.Symlink(directory, workspace); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	paths, err := json.Marshal([]string{launcher})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "up", "agent01", workspace)
	command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{
		"SANDBOXED_AGENTS_NPM_LAUNCHERS": string(paths),
	})
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "step 1:") || !strings.Contains(string(output), "protected host path") {
		t.Fatalf("npm launcher workspace: error=%v output=%s; want protected-path refusal at step 1", err, output)
	}
	assertNoExternalCalls(t, fakes)
}
