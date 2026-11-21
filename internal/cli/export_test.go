package cli

import (
	"io"
	"time"
)

// SetReadinessDeadline shortens the readiness wait for tests that drive the
// executable boundary, where no clock can be injected.
func SetReadinessDeadline(deadline time.Duration) {
	readinessDeadline = deadline
}

// SetMachineQueryTimeout shortens the bound of one machine query's ssh client for tests that drive
// a client that never answers, and returns a function that restores the previous bound.
func SetMachineQueryTimeout(timeout time.Duration) func() {
	previous := machineQueryTimeout
	machineQueryTimeout = timeout
	return func() { machineQueryTimeout = previous }
}

// SetToolStatusTimeouts shortens the limits of the host's Tool status query for tests that drive the
// executable boundary, and returns a function that restores the previous limits.
func SetToolStatusTimeouts(records, upstream time.Duration) func() {
	previousRecords, previousUpstream := toolRecordQueryTimeout, toolUpstreamQueryTimeout
	toolRecordQueryTimeout, toolUpstreamQueryTimeout = records, upstream
	return func() { toolRecordQueryTimeout, toolUpstreamQueryTimeout = previousRecords, previousUpstream }
}

// HoldImageTagLock takes the image-tag lock for tag on platform's host-state root, for tests that
// stand in for another command holding that tag while a CLI run waits on it.
func HoldImageTagLock(platform, tag string) (io.Closer, error) {
	return acquireImageTagLock(podmanTarget{host: Host{Platform: platform}}, tag, io.Discard)
}
