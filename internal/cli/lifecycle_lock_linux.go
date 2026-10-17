package cli

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// lockLifecycleFile acquires a nonblocking exclusive Linux advisory lock on the file.
func lockLifecycleFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// lifecycleLockBusy reports whether a Linux lock attempt failed because the lock was already
// held.
func lifecycleLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
