//go:build !linux && !windows

package main_test

import (
	"testing"
	"time"
)

// liveTerminal has no pseudo-terminal implementation outside Linux and Windows.
type liveTerminal struct{}

func startLiveTerminal(t *testing.T, environment []string, program string, args ...string) *liveTerminal {
	t.Skip("pseudo-terminal sessions are supported on Linux and Windows only")
	return nil
}

func (terminal *liveTerminal) Write(string) error             { return nil }
func (terminal *liveTerminal) Output() string                 { return "" }
func (terminal *liveTerminal) Wait(time.Duration) (int, bool) { return 0, false }
func (terminal *liveTerminal) Close()                         {}
