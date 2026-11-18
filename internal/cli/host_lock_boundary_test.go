package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWaitingHostLockNamesOSFailures proves each lock kind retains its error context and cause
// for both the nonblocking attempt and the blocking wait at the injected OS-lock boundary.
func TestWaitingHostLockNamesOSFailures(t *testing.T) {
	for _, kind := range []string{"image-tag", "port-allocation"} {
		for _, stage := range []string{"attempt", "wait"} {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				dir := t.TempDir()
				cause := errors.New("OS lock failure")
				var progress bytes.Buffer
				var attempted *os.File
				tryLock := func(file *os.File) error { attempted = file; return cause }
				waitLock := func(file *os.File) error { t.Error("unexpected blocking wait"); return nil }
				if stage == "wait" {
					holder, err := acquireHostLock(dir, "resource", kind)
					if err != nil {
						t.Fatal(err)
					}
					defer holder.Close()
					tryLock = tryFileLock
					waitLock = func(file *os.File) error { attempted = file; return cause }
				}
				file, err := acquireHostLockWaitingWith(dir, "resource", kind, &progress, tryLock, waitLock)
				if file != nil {
					file.Close()
					t.Fatal("failed acquire returned an open lock")
				}
				if err == nil || err.Error() != "lock "+kind+": OS lock failure" || !errors.Is(err, cause) {
					t.Errorf("error=%v want contextual %s failure retaining its cause", err, kind)
				}
				if attempted == nil {
					t.Fatal("OS lock boundary was not reached")
				}
				if _, err := attempted.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Errorf("failed descriptor not closed: %v", err)
				}
				info, statErr := os.Stat(filepath.Join(dir, "resource"))
				if statErr != nil || !info.Mode().IsRegular() {
					t.Fatalf("lock file not retained: %v", statErr)
				}
				wantNotices := 0
				if stage == "wait" {
					wantNotices = 1
				}
				if got := strings.Count(progress.String(), "waiting for "+kind+" lock: "); got != wantNotices {
					t.Errorf("notices=%d want %d: %q", got, wantNotices, progress.String())
				}
			})
		}
	}
}
