package manualcheck_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// TestInstallerTrustedSnapshotOnWindows proves the admin-only trusted snapshot and the protected output
// directory of the installer package against real Windows security descriptors, accounts, links, and
// handles: the protected descriptor they get, the item account's read-only grant, the refusal of
// inherited, granted, and user-owned access, of junctions, and of ancestors that let another account
// replace them, the account checks, and the bound-input reader's refusal of junctions, symbolic links,
// and hard links. It runs test_trusted_snapshot_windows.py, which drives the real PowerShell with only
// the trusted base substituted. It runs on Windows only, in an elevated process; with
// SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1, as in the Windows CI job, a missing precondition fails
// instead of skipping, and any skipped test fails the run.
func TestInstallerTrustedSnapshotOnWindows(t *testing.T) {
	strict := os.Getenv("SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT") == "1"
	if runtime.GOOS != "windows" {
		t.Skip("real Windows security descriptors need Windows")
	}
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		if strict {
			t.Fatal("SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 but Python 3 is missing")
		}
		t.Skip("the trusted-snapshot tests require Python 3")
	}
	command := exec.Command(python, "-I", "-B", "-m", "unittest", "discover", "-v", "-s", "tests", "-p", "test_trusted_snapshot_windows.py")
	command.Dir = filepath.Join("windows", "installers")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("trusted-snapshot tests failed: %v\n%s", err, output)
	}
	if strict && strings.Contains(string(output), "skipped") {
		t.Fatalf("SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 but tests were skipped:\n%s", output)
	}
	t.Logf("%s", output)
}

// lastLines returns the final n lines of text.
func lastLines(text string, n int) string {
	lines := splitLines(text)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return joinLines(lines)
}
