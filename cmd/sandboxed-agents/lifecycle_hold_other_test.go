//go:build !windows

package main_test

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive takes the exclusive lock that the Unix lifecycle lock takes, on this descriptor, so another descriptor in the same process meets the contention.
func lockFileExclusive(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
