package cli

import "time"

// SetReadinessDeadline shortens the readiness wait for tests that drive the
// executable boundary, where no clock can be injected. It returns a restore function.
func SetReadinessDeadline(deadline time.Duration) func() {
	previous := readinessDeadline
	readinessDeadline = deadline
	return func() { readinessDeadline = previous }
}
