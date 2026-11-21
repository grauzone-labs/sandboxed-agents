package capture_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if len(entries) != 1 || !ok || fields[2] != pid || fields[19] != tick {
		t.Fatalf("record %q does not name a running group leader: %s", entries[0].Name(), data)
	}
	if err := os.WriteFile(releaseOf(t, command), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// RunOwned has returned, so reading the started process no longer races with it.
	if pid != strconv.Itoa(command.Process.Pid) {
		t.Fatalf("record %q names another process than the invocation %d", entries[0].Name(), command.Process.Pid)
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

// TestOwnedGateCallerFixture is a tracked RunOwned caller that dies after its
// command started but before the command's group is recorded; its parent
// checks that the command never runs. It returns at once when unconfigured.
func TestOwnedGateCallerFixture(t *testing.T) {
	directory := os.Getenv("CAPTURE_TEST_GATE_RECORDS")
	if directory == "" {
		return
	}
	capture.TrackOwnedGroups(directory)
	capture.SetOwnedRecordHook(func(pid int) {
		_ = os.WriteFile(os.Getenv("CAPTURE_TEST_GATE_PID"), []byte(strconv.Itoa(pid)), 0600)
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	})
	command := exec.Command("/bin/sh", "-c", `: > "$1"; exec sleep 30`, "sh", os.Getenv("CAPTURE_TEST_GATE_MARKER"))
	_ = capture.RunOwned(context.Background(), command, nil, nil)
	t.Fatal("the caller outlived its own kill")
}

// TestRunOwnedGatesCommandUntilRecorded proves a tracked RunOwned command does
// not run before its process group is recorded: when the caller dies between
// starting the command and recording it, the command never executes and its
// process ends. It exercises the RunOwned start gate through a nested caller
// and the started process's state.
func TestRunOwnedGatesCommandUntilRecorded(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	root := t.TempDir()
	records, pidFile, marker := filepath.Join(root, "records"), filepath.Join(root, "pid"), filepath.Join(root, "marker")
	if err := os.Mkdir(records, 0700); err != nil {
		t.Fatal(err)
	}
	caller := exec.Command(os.Args[0], "-test.run=^TestOwnedGateCallerFixture$")
	caller.Env = append(os.Environ(), "CAPTURE_TEST_GATE_RECORDS="+records, "CAPTURE_TEST_GATE_PID="+pidFile, "CAPTURE_TEST_GATE_MARKER="+marker)
	output, err := caller.CombinedOutput()
	if status, ok := err.(*exec.ExitError); !ok || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("caller did not die at the hook: %v\n%s", err, output)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	assertOwnedProcessExited(t, pid)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the command ran before its group was recorded: %v", err)
	}
	if entries, err := os.ReadDir(records); err != nil || len(entries) != 0 {
		t.Fatalf("records after the caller died before recording: %v %v", entries, err)
	}
}

// TestRunOwnedTrackedCommandSeesUnchangedProcess proves the start gate of a
// tracked RunOwned call leaves the command's argv, environment, open
// descriptors, signal state, standard streams and exit status exactly as an
// untracked call gives them. It exercises the TrackOwnedGroups and RunOwned
// process seam on Linux.
func TestRunOwnedTrackedCommandSeesUnchangedProcess(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	script := `tr '\0' ' ' < /proc/$$/cmdline; echo; env | sort; ls /proc/$$/fd; grep -E '^Sig(Blk|Ign)' /proc/$$/status; read -r line; echo "input $line"; echo failure >&2; exit 3`
	run := func(tracked bool) (string, string, error) {
		t.Helper()
		if tracked {
			capture.TrackOwnedGroups(t.TempDir())
			defer capture.TrackOwnedGroups("")
		}
		command := exec.Command("/bin/sh", "-c", script, "name")
		command.Args[0] = "custom-argv0"
		command.Env = []string{"KEEP=value", "PATH=/usr/bin:/bin"}
		command.Dir = t.TempDir()
		command.Stdin = strings.NewReader("given\n")
		var stdout, stderr strings.Builder
		err := capture.RunOwned(context.Background(), command, &stdout, &stderr)
		return strings.ReplaceAll(stdout.String(), command.Dir, "DIR"), stderr.String(), err
	}
	wantOut, wantErr, wantStatus := run(false)
	gotOut, gotErr, gotStatus := run(true)
	var want, got *exec.ExitError
	if !errors.As(wantStatus, &want) || !errors.As(gotStatus, &got) || want.ExitCode() != 3 || got.ExitCode() != 3 {
		t.Fatalf("exit status: untracked %v, tracked %v", wantStatus, gotStatus)
	}
	if gotOut != wantOut || gotErr != wantErr || !strings.HasPrefix(gotOut, "custom-argv0 -c ") {
		t.Fatalf("tracked command differs:\nuntracked %q %q\ntracked   %q %q", wantOut, wantErr, gotOut, gotErr)
	}
}

// TestRunOwnedTrackedStartFailureMatchesStart proves every tracked RunOwned
// command starts behind the gate, including one exec refuses, and that such a
// refusal returns exactly the error an untracked Start returns, with nothing
// left recorded. It exercises the TrackOwnedGroups and RunOwned start seam on
// Linux.
func TestRunOwnedTrackedStartFailureMatchesStart(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	garbage := filepath.Join(root, "garbage")
	if err := os.WriteFile(plain, []byte("data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(garbage, []byte("not a program\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		path  string
		errno syscall.Errno
	}{
		{"not executable", plain, syscall.EACCES},
		{"unknown format", garbage, syscall.ENOEXEC},
		{"directory", root, syscall.EACCES},
		{"missing", filepath.Join(root, "missing"), syscall.ENOENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			untracked := capture.RunOwned(context.Background(), exec.Command(tc.path, "argument"), nil, nil)
			records := t.TempDir()
			gated := 0
			capture.TrackOwnedGroups(records)
			capture.SetOwnedRecordHook(func(int) { gated++ })
			t.Cleanup(func() {
				capture.TrackOwnedGroups("")
				capture.SetOwnedRecordHook(nil)
			})
			var stdout, stderr strings.Builder
			tracked := capture.RunOwned(context.Background(), exec.Command(tc.path, "argument"), &stdout, &stderr)
			var path *os.PathError
			if !errors.As(tracked, &path) || path.Op != "fork/exec" || path.Path != tc.path || !errors.Is(tracked, tc.errno) || untracked == nil || tracked.Error() != untracked.Error() {
				t.Fatalf("tracked start failure %v; untracked %v", tracked, untracked)
			}
			if gated != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("gated=%d stdout=%q stderr=%q; want one gated start and no output", gated, stdout.String(), stderr.String())
			}
			if entries, err := os.ReadDir(records); err != nil || len(entries) != 0 {
				t.Fatalf("refused start stayed recorded: %v %v", entries, err)
			}
		})
	}
}

// TestRunOwnedCancellationEndsStalledGate proves cancelling a tracked RunOwned
// call ends a gate that was released but stalls before it executes the
// command: RunOwned returns the cancellation, the group is gone and its record
// removed. It exercises the RunOwned start gate and cancellation seam on Linux.
func TestRunOwnedCancellationEndsStalledGate(t *testing.T) {
	records := t.TempDir()
	capture.TrackOwnedGroups(records)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := 0
	// Stopping the gate before its release byte arrives leaves it released but unable to exec.
	capture.SetOwnedRecordHook(func(pid int) {
		gate = pid
		_ = syscall.Kill(pid, syscall.SIGSTOP)
		time.AfterFunc(200*time.Millisecond, cancel)
	})
	t.Cleanup(func() {
		capture.TrackOwnedGroups("")
		capture.SetOwnedRecordHook(nil)
	})
	done := make(chan error, 1)
	go func() { done <- capture.RunOwned(ctx, exec.Command("/bin/sleep", "30"), nil, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stalled gate returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt the stalled gate")
	}
	assertOwnedProcessExited(t, gate)
	if entries, err := os.ReadDir(records); err != nil || len(entries) != 0 {
		t.Fatalf("cancelled gate stayed recorded: %v %v", entries, err)
	}
}
