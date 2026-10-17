package cli

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

// lockLifecycleFile acquires a nonblocking exclusive Windows lock on the file's first byte.
func lockLifecycleFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// lifecycleLockBusy reports whether a Windows lock attempt failed because the lock was already
// held.
func lifecycleLockBusy(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
