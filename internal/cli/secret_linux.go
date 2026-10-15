package cli

import (
	"golang.org/x/sys/unix"
	"os"
)

func hideTerminalInput(file *os.File) (func() error, error) {
	fd := int(file.Fd())
	previous, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	mode := *previous
	mode.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG
	mode.Cc[unix.VMIN] = 1
	mode.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &mode); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, unix.TCSETS, previous) }, nil
}
