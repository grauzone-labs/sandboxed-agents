package cli

import (
	"golang.org/x/sys/windows"
	"os"
)

// hideTerminalInput disables console echo and line processing and returns a function that
// restores the prior mode.
func hideTerminalInput(file *os.File) (func() error, error) {
	handle := windows.Handle(file.Fd())
	var previous uint32
	if err := windows.GetConsoleMode(handle, &previous); err != nil {
		return nil, err
	}
	mode := previous &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(handle, mode); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(handle, previous) }, nil
}
