package manager

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockManager(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another agent change is in progress; retry")
		}
		return nil, err
	}
	return func() { unix.Flock(int(file.Fd()), unix.LOCK_UN); file.Close() }, nil
}
