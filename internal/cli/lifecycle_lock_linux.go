package cli

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func lockLifecycleFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func lifecycleLockBusy(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
