package main_test

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes the exclusive lock that the Windows lifecycle lock takes, on this handle, so another handle in the same process meets the contention.
func lockFileExclusive(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
