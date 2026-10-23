package manager

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockManager acquires the exclusive nonblocking manager lock and returns its release function.
func lockManager(path string) (func(), error) {
	release, held, err := tryLock(path)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, fmt.Errorf("another agent or tool change is in progress; retry")
	}
	return release, nil
}

// tryLock takes an exclusive nonblocking advisory lock on the file, creating it when missing, and
// reports whether it was taken; a taken lock is released by the returned function.
func tryLock(path string) (release func(), held bool, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }, true, nil
}

// exclusivelyLocked reports whether another process holds the file's exclusive lock. The probe takes
// a shared lock for an instant, so probes never conflict with each other, only with the exclusive holder.
func exclusivelyLocked(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return false, nil
}
