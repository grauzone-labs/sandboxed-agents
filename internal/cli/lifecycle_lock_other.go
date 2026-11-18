//go:build !linux && !windows

package cli

import (
	"fmt"
	"os"
)

// lockLifecycleFile reports that lifecycle locking requires a supported host platform.
func lockLifecycleFile(file *os.File) error {
	return fmt.Errorf("lifecycle locking requires Linux or Windows")
}

// lifecycleLockBusy reports no busy-lock condition because this platform cannot acquire
// lifecycle locks.
func lifecycleLockBusy(err error) bool { return false }

// lockFileWaiting reports that blocking host locking requires a supported host platform.
func lockFileWaiting(file *os.File) error {
	return fmt.Errorf("blocking host locking requires Linux or Windows")
}

// tryFileLock reports that blocking host locking requires a supported host platform.
func tryFileLock(file *os.File) error {
	return lockFileWaiting(file)
}
