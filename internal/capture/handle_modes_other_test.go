//go:build !windows

package capture_test

// handleModes reports that handle completion modes are a Windows concept only.
func handleModes() string {
	return "stdout=unsupported stderr=unsupported"
}

// inputHandleMode reports that asynchronous handle modes are a Windows concept only.
func inputHandleMode() string { return "unsupported" }
