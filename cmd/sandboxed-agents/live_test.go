package main_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func TestLivePodmanVersion(t *testing.T) {
	if os.Getenv(testutil.LiveTestsEnv) != "1" {
		t.Skip("set " + testutil.LiveTestsEnv + "=1 to opt in")
	}
	t.Log("live test body selected")
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatalf("Podman is unavailable: %v", err)
	}
	if output, err := exec.Command("podman", "version").CombinedOutput(); err != nil {
		t.Fatalf("Podman is unusable: %v: %s", err, output)
	}
}
