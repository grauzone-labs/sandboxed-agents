package cli

import "time"

// SetReadinessDeadline shortens the readiness wait for tests that drive the
// executable boundary, where no clock can be injected.
func SetReadinessDeadline(deadline time.Duration) {
	readinessDeadline = deadline
}
