//go:build !windows

package capture_test

// handleModes reports that handle completion modes are a Windows concept only.
func handleModes() string {
	return "stdout=unsupported stderr=unsupported"
}
