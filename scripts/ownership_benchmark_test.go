package scripts_test

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestOwnershipBenchmarkCommand proves the isolated benchmark measures its supplied image and
// refuses unsafe cleanup, active-store paths, low headroom and timeouts through executable fixtures.
func TestOwnershipBenchmarkCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ownership benchmark is Linux-only")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3.11+ is required for the optional ownership benchmark")
	}
	prerequisite := exec.Command(python, "-c", "import os,sys; reason = 'Python 3.11+ is required' if sys.version_info < (3,11) else ('a non-root user is required' if os.geteuid() == 0 else ''); print(reason); sys.exit(77 if reason else 0)")
	if output, err := prerequisite.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 77 {
			t.Skip("optional ownership benchmark: " + strings.TrimSpace(string(output)))
		}
		t.Fatalf("benchmark prerequisite check: %v\n%s", err, output)
	}
	command := exec.Command(python, "testdata/test_ownership_benchmark.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("benchmark command: %v\n%s", err, output)
	}
}
