//go:build !linux && !windows

package capture_test

import "testing"

// assertOwnedProcessExited reports that owned process supervision is unsupported on this host.
func assertOwnedProcessExited(t *testing.T, pid int) {
	t.Helper()
	t.Skip("owned process supervision requires Linux or Windows")
}
