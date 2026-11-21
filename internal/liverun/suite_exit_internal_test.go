package liverun

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
)

// TestSuiteExitCodeRecordsDeadlineKillAs137 proves a suite ended by the outer
// deadline is recorded as 137 whatever exit status its killed process left,
// including the status 0 that closing a Windows kill-on-close job reports, and
// that an undisturbed suite keeps its own status. It exercises the suite exit
// status seam of the supervisor.
func TestSuiteExitCodeRecordsDeadlineKillAs137(t *testing.T) {
	failing := exec.Command("sh", "-c", "exit 7")
	if runtime.GOOS == "windows" {
		failing = exec.Command("cmd", "/c", "exit 7")
	}
	exitErr := failing.Run()
	var exit *exec.ExitError
	if !errors.As(exitErr, &exit) {
		t.Fatalf("fixture exit = %v; want an exit status", exitErr)
	}
	for _, tc := range []struct {
		name     string
		err      error
		timedOut bool
		want     int
	}{
		{"deadline with status 0", nil, true, 137},
		{"deadline with own status", exitErr, true, 137},
		{"deadline with start error", errors.New("wait failed"), true, 137},
		{"success", nil, false, 0},
		{"own status", exitErr, false, 7},
		{"wait error", errors.New("wait failed"), false, 1},
	} {
		if got := suiteExitCode(tc.err, tc.timedOut); got != tc.want {
			t.Errorf("%s: suiteExitCode = %d; want %d", tc.name, got, tc.want)
		}
	}
}
