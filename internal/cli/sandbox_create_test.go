package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// progressRecorder collects progress lines written from the create watcher's timer goroutine and
// signals the first write.
type progressRecorder struct {
	mu      sync.Mutex
	text    strings.Builder
	written chan struct{}
	once    sync.Once
}

// newProgressRecorder returns an empty recorder whose written channel closes on the first write.
func newProgressRecorder() *progressRecorder {
	return &progressRecorder{written: make(chan struct{})}
}

// Write appends data under the recorder's lock and closes the written channel once.
func (r *progressRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text.Write(data)
	r.once.Do(func() { close(r.written) })
	return len(data), nil
}

// String returns everything written so far.
func (r *progressRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

// steppedClock returns start on its first call and start+step on every later call, so elapsed
// times in messages are exact without real waiting.
func steppedClock(start time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	calls := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(step)
	}
}

// testSandboxCreate returns a create description for sandbox agent01 with the given threshold,
// progress writer, and clock.
func testSandboxCreate(after time.Duration, progress *progressRecorder, now func() time.Time) sandboxCreate {
	return sandboxCreate{
		sandbox:       "agent01",
		container:     "sandboxed-agents.default.agent01",
		image:         "localhost/sandboxed-agents:none",
		check:         "podman ps --all --filter name=sandboxed-agents.default.agent01",
		progressAfter: after,
		progress:      progress,
		now:           now,
	}
}

// TestSandboxCreateReportsSlowCreateOnce proves a create still running past the injected
// threshold writes exactly one progress line, keeps running, and returns its own result, through
// sandboxCreate.run with an injected threshold, writer, and clock.
func TestSandboxCreateReportsSlowCreateOnce(t *testing.T) {
	progress := newProgressRecorder()
	create := testSandboxCreate(10*time.Millisecond, progress, steppedClock(time.Unix(0, 0), 2*time.Minute))
	ran := false
	err := create.run(context.Background(), func(context.Context) error {
		select {
		case <-progress.written:
		case <-time.After(2 * time.Second):
			return errors.New("no progress line while the create ran")
		}
		// Stay running for several more thresholds; the line must not repeat.
		time.Sleep(50 * time.Millisecond)
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	want := "sandbox agent01: podman create of container sandboxed-agents.default.agent01 is still running after 2m0s; " +
		"a first create of a new image can take several minutes while Podman prepares the user-namespace layer copy; " +
		"the create continues; check it with: podman ps --all --filter name=sandboxed-agents.default.agent01 (image localhost/sandboxed-agents:none)\n"
	if got := progress.String(); got != want {
		t.Fatalf("progress output:\n%q\nwant:\n%q", got, want)
	}
}

// TestSandboxCreateFastCreateWritesNoProgress proves a create that finishes before the threshold
// writes nothing to the progress writer, through sandboxCreate.run with an injected threshold.
func TestSandboxCreateFastCreateWritesNoProgress(t *testing.T) {
	progress := newProgressRecorder()
	create := testSandboxCreate(time.Minute, progress, time.Now)
	if err := create.run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := progress.String(); got != "" {
		t.Fatalf("fast create wrote progress %q", got)
	}
}

// TestSandboxCreateTimeoutNamesStepAndElapsed proves a create ended by its context deadline
// returns an error naming the sandbox, the create step, and the elapsed time while keeping the
// context error, through sandboxCreate.run with an injected clock and an expired context.
func TestSandboxCreateTimeoutNamesStepAndElapsed(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	create := testSandboxCreate(time.Hour, newProgressRecorder(), steppedClock(time.Unix(0, 0), 61*time.Minute))
	err := create.run(ctx, func(ctx context.Context) error {
		return fmt.Errorf("podman create --name sandboxed-agents.default.agent01: %w (signal: killed): ", ctx.Err())
	})
	want := "sandbox agent01: create step (podman create of container sandboxed-agents.default.agent01 from image localhost/sandboxed-agents:none) ended after 1h1m0s without finishing: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v\nwant prefix %q wrapping %v", err, want, context.DeadlineExceeded)
	}
}

// TestSandboxCreateSignalNamesStepAndElapsed proves a create whose Podman process a signal ended
// returns the timeout error with the step and elapsed time, through sandboxCreate.run with a real
// signal-terminated child process.
func TestSandboxCreateSignalNamesStepAndElapsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal termination is a POSIX process outcome")
	}
	create := testSandboxCreate(time.Hour, newProgressRecorder(), steppedClock(time.Unix(0, 0), 23*time.Minute+24*time.Second))
	err := create.run(context.Background(), func(context.Context) error {
		return fmt.Errorf("podman create: %w", exec.Command("sh", "-c", "kill -KILL $$").Run())
	})
	want := "sandbox agent01: create step (podman create of container sandboxed-agents.default.agent01 from image localhost/sandboxed-agents:none) ended after 23m24s without finishing: podman create: signal: killed"
	if err == nil || err.Error() != want {
		t.Fatalf("err=%v\nwant %q", err, want)
	}
}

// TestSandboxCreateKeepsOtherErrors proves a create that fails with an exit status, not a timeout,
// returns its error unchanged, through sandboxCreate.run.
func TestSandboxCreateKeepsOtherErrors(t *testing.T) {
	cause := errors.New("podman create: exit status 125: image not known")
	create := testSandboxCreate(time.Hour, newProgressRecorder(), time.Now)
	if err := create.run(context.Background(), func(context.Context) error { return cause }); err != cause {
		t.Fatalf("err=%v, want unchanged %v", err, cause)
	}
}
