//go:build !windows

package cli

import "fmt"

// windowsBuild reports that Windows build queries are unavailable on this platform.
func windowsBuild() (uint32, error) {
	return 0, fmt.Errorf("Windows build query is unavailable on this platform")
}

// windowsWorkstation reports that Windows product queries are unavailable on this platform.
func windowsWorkstation() (bool, error) {
	return false, fmt.Errorf("Windows product query is unavailable on this platform")
}

// windowsNativeArchitecture reports that Windows native architecture queries are unavailable on
// this platform.
func windowsNativeArchitecture() (uint16, error) {
	return 0, fmt.Errorf("Windows native architecture query is unavailable on this platform")
}
