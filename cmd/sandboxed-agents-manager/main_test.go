//go:build linux

package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagerBinaryVersionAndUsage proves that the thin manager main passes arguments, streams, and exit status through its built executable without starting sandbox programs.
func TestManagerBinaryVersionAndUsage(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "sandboxed-agents-manager")
	if output, err := exec.Command("go", "build", "-o", executable, ".").CombinedOutput(); err != nil {
		t.Fatalf("build manager: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		args           []string
		status         int
		stdout, stderr string
	}{
		{[]string{"version"}, 0, "sandboxed-agents-manager development\n", ""},
		{[]string{"unknown"}, 2, "", "usage: sandboxed-agents-manager version | sessions | ssh-host-key | ssh-authorize KEY | ssh-deauthorize\n"},
	} {
		command := exec.Command(executable, tc.args...)
		// Even accidental external program lookup cannot reach a host executable.
		command.Env = []string{"PATH=" + t.TempDir()}
		var stdout, stderr strings.Builder
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		status := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				status = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if status != tc.status || stdout.String() != tc.stdout || stderr.String() != tc.stderr {
			t.Errorf("args=%q status=%d stdout=%q stderr=%q", tc.args, status, stdout.String(), stderr.String())
		}
	}
}
