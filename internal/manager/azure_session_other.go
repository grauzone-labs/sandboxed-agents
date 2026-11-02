//go:build !linux

package manager

import "io"

// azureSession refuses sandbox session supervision on platforms without the Linux manager runtime.
func azureSession(token, sshHost string, stdout, stderr io.Writer, effects Effects) int { return 1 }

// azureControl refuses sandbox session control on platforms without the Linux manager runtime.
func azureControl(token, operation string, stderr io.Writer, effects Effects) int { return 1 }
