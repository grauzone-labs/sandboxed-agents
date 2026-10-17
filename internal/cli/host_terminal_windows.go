//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether the file is a Windows console handle.
func isTerminal(file *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) == nil
}
