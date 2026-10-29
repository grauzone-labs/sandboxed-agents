package capture_test

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// assertOwnedProcessExited verifies that the actual descendant process handle is signalled.
func assertOwnedProcessExited(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("missing descendant PID")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 1000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned descendant not exited: state=%d error=%v", state, err)
	}
}
