//go:build windows

package manualcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestProtectedInstallerOutputOnWindows proves verifyProtectedOutput, the check `installers summarize`
// applies before it reads a capture run's output directory, against real Windows descriptors: it
// accepts a directory chain owned by Administrators, and refuses an output directory the current
// account owns or one that is a symbolic link. Assigning Administrators as owner needs an elevated
// process; with SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1, as in the Windows CI job, a process that is
// not elevated fails the test instead of skipping it.
func TestProtectedInstallerOutputOnWindows(t *testing.T) {
	if elevated, err := readProcessElevated(); err != nil || !elevated {
		if os.Getenv("SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT") == "1" {
			t.Fatalf("SANDBOXED_AGENTS_TEST_TRUSTED_SNAPSHOT=1 but the process is not elevated (%v)", err)
		}
		t.Skip("assigning Administrators as owner needs an elevated process")
	}
	base := filepath.Join(t.TempDir(), "sandboxed-agents-manual")
	output := filepath.Join(base, "output", "npm-test-1")
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, filepath.Dir(output), output} {
		setOwner(t, path, admins)
	}
	if err := verifyProtectedOutput(output); err != nil {
		t.Fatalf("an output chain owned by Administrators was refused: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	setOwner(t, output, user.User.Sid)
	if err := verifyProtectedOutput(output); err == nil || !strings.Contains(err.Error(), "not owned by Administrators or SYSTEM") {
		t.Fatalf("an output directory owned by the current account was accepted: %v", err)
	}
	setOwner(t, output, admins)
	linked := filepath.Join(base, "output", "npm-linked")
	if err := os.Symlink(output, linked); err != nil {
		t.Fatal(err)
	}
	if err := verifyProtectedOutput(linked); err == nil || !strings.Contains(err.Error(), "is a link or not a directory") {
		t.Fatalf("a linked output directory was accepted: %v", err)
	}
}

// setOwner makes sid the owner of path.
func setOwner(t *testing.T, path string, sid *windows.SID) {
	t.Helper()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, sid, nil, nil, nil); err != nil {
		t.Fatalf("set the owner of %s: %v", path, err)
	}
}
