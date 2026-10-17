//go:build !linux && !windows

package main_test

import (
	"testing"
	"time"
)

// liveTerminal has no pseudo-terminal implementation outside Linux and Windows.
type liveTerminal struct{}

// startLiveTerminal skips live terminal tests on platforms without pseudo-terminal support.
func startLiveTerminal(t *testing.T, environment []string, program string, args ...string) *liveTerminal {
	t.Skip("pseudo-terminal sessions are supported on Linux and Windows only")
	return nil
}

// Write is inert because terminal startup skips tests on this platform.
func (terminal *liveTerminal) Write(string) error { return nil }

// Output returns no captured output on an unsupported terminal platform.
func (terminal *liveTerminal) Output() string { return "" }

// Wait reports no completed process on an unsupported terminal platform.
func (terminal *liveTerminal) Wait(time.Duration) (int, bool) { return 0, false }

// Close is inert because no terminal is created on this platform.
func (terminal *liveTerminal) Close() {}
