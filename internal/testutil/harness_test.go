package testutil_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func TestHarnessChildEnvironmentCannotFindProgramsOnOriginalPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("marker script uses a POSIX shebang")
	}
	originalDir := t.TempDir()
	marker := filepath.Join(originalDir, "podman")
	if err := os.WriteFile(marker, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", originalDir)
	if path, err := exec.LookPath("podman"); err != nil || path != marker {
		t.Fatalf("marker setup: path=%q error=%v; want %q", path, err, marker)
	}
	// Leave the fake directory empty to exercise a missing fake binary.
	fakes := testutil.Fakes{Dir: t.TempDir()}
	environment := fakes.Env(os.Environ())
	var paths []string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			paths = append(paths, value)
		}
	}
	if len(paths) != 1 || paths[0] != fakes.Dir {
		t.Errorf("child PATH values=%q; want only %q", paths, fakes.Dir)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestHarnessPATHChild$")
	child.Env = testutil.Env(environment, map[string]string{"TEST_HARNESS_PATH_CHILD": "1"})
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated child: %v\n%s", err, output)
	}
}

func TestHarnessPATHChild(t *testing.T) {
	if os.Getenv("TEST_HARNESS_PATH_CHILD") != "1" {
		t.Skip("child process helper")
	}
	if path, err := exec.LookPath("podman"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("podman lookup: path=%q error=%v; want executable not found", path, err)
	}
}
