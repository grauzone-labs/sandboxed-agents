//go:build !linux

package manager

import (
	"fmt"
	"io"
)

// azureSession verifies agent identity before explaining that session supervision requires the Linux manager runtime.
func azureSession(token, sshHost string, stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	fmt.Fprintln(stderr, "Azure browser sessions require the Linux manager runtime")
	return 1
}

// azureControl verifies agent identity before explaining that session control requires the Linux manager runtime.
func azureControl(token, operation string, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	fmt.Fprintln(stderr, "Azure browser session control requires the Linux manager runtime")
	return 1
}
