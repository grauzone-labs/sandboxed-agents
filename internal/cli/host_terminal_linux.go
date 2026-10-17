//go:build linux

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether the file supports Linux terminal attribute queries.
func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}
