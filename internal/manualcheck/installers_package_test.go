package manualcheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestInstallerHelperSuite proves the Windows installer evidence helpers (the Procmon trace
// reviewer, the environment reviewer, the readiness guards, the kernel-trace reader, the raw-log
// reader of pipe information queries, and the process identity guard) through their Python test
// suite, which drives the real PowerShell scripts with synthetic exports, synthetic saved logs, and
// substituted operating-system boundaries only.
func TestInstallerHelperSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("the installer helper suite takes a few minutes; skipped with -short")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("the installer helper suite requires Python 3")
	}
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("the installer helper suite requires PowerShell 7 (pwsh)")
	}
	if runtime.GOOS == "windows" && os.Getenv("SANDBOXED_AGENTS_TEST_INSTALLER_HELPERS") != "1" {
		t.Skip("on Windows the suite runs with SANDBOXED_AGENTS_TEST_INSTALLER_HELPERS=1; it is verified on Linux")
	}
	directory := filepath.Join("windows", "installers")
	// -I isolates the interpreter from PYTHON* variables, so -B is what keeps it from writing
	// bytecode caches into the pinned package directory.
	command := exec.Command(python, "-I", "-B", "-m", "unittest", "discover", "-s", "tests")
	command.Dir = directory
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installer helper suite failed: %v\n%s", err, output)
	}
	t.Logf("%s", lastLines(string(output), 4))
}

// lastLines returns the final n lines of text.
func lastLines(text string, n int) string {
	lines := splitLines(text)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return joinLines(lines)
}
