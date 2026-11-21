package capture_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// assertOwnedProcessExited distinguishes a dead or unreaped zombie process from running work.
func assertOwnedProcessExited(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatal("missing descendant PID")
	}
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		_, tail, ok := strings.Cut(string(data), ") ")
		if ok && strings.HasPrefix(tail, "Z ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned descendant still exists: %s", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// releaseOf returns the release file a helper command waits for.
func releaseOf(t *testing.T, command *exec.Cmd) string {
	t.Helper()
	for _, entry := range command.Env {
		if value, ok := strings.CutPrefix(entry, helperRelease+"="); ok {
			return value
		}
	}
	t.Fatal("helper command has no release file")
	return ""
}

// TestRunOwnedRecordsRunningGroup proves a tracked RunOwned call records its
// running process group as PID.STARTTICK, naming the group leader's own start
// tick, and removes the record once the call returns. It exercises the
// TrackOwnedGroups and RunOwned process-group seam on Linux.
func TestRunOwnedRecordsRunningGroup(t *testing.T) {
	directory := t.TempDir()
	capture.TrackOwnedGroups(directory)
	t.Cleanup(func() { capture.TrackOwnedGroups("") })
	command := helper(context.Background(), t, "wait")
	command.Cancel = nil
	done := make(chan error, 1)
	go func() { done <- capture.RunOwned(context.Background(), command, nil, nil) }()
	var entries []os.DirEntry
	deadline := time.Now().Add(5 * time.Second)
	for len(entries) == 0 {
		var err error
		if entries, err = os.ReadDir(directory); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("running owned group was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, tick, ok := strings.Cut(entries[0].Name(), ".")
	data, err := os.ReadFile(fmt.Sprintf("/proc/%s/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data[strings.LastIndex(string(data), ")")+1:]))
	if len(entries) != 1 || !ok || pid != strconv.Itoa(command.Process.Pid) || fields[2] != pid || fields[19] != tick {
		t.Fatalf("record %q does not name the running group leader %d: %s", entries[0].Name(), command.Process.Pid, data)
	}
	if err := os.WriteFile(releaseOf(t, command), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if entries, err = os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("finished owned group stayed recorded: %v %v", entries, err)
	}
}

// TestRunOwnedRefusesUnrecordableGroup proves a tracked RunOwned call whose
// group record cannot be written ends the started invocation and reports the
// failure instead of running untracked. It exercises the TrackOwnedGroups and
// RunOwned process-group seam on Linux.
func TestRunOwnedRefusesUnrecordableGroup(t *testing.T) {
	capture.TrackOwnedGroups(filepath.Join(t.TempDir(), "missing"))
	t.Cleanup(func() { capture.TrackOwnedGroups("") })
	command := helper(context.Background(), t, "wait")
	command.Cancel = nil
	started := time.Now()
	err := capture.RunOwned(context.Background(), command, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "record owned process group") || time.Since(started) > 5*time.Second {
		t.Fatalf("err = %v after %v; want a prompt record failure", err, time.Since(started))
	}
	assertOwnedProcessExited(t, command.Process.Pid)
}
