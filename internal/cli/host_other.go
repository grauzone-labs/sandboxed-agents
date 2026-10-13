//go:build !windows

package cli

import "fmt"

func windowsBuild() (uint32, error) {
	return 0, fmt.Errorf("Windows build query is unavailable on this platform")
}

func windowsWorkstation() (bool, error) {
	return false, fmt.Errorf("Windows product query is unavailable on this platform")
}

var windowsNativeArchitecture = func() (uint16, error) {
	return 0, fmt.Errorf("Windows native architecture query is unavailable on this platform")
}
