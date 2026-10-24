package capture_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// helperMode names the environment variable that turns the test executable into a child fixture.
const helperMode = "CAPTURE_TEST_HELPER_MODE"

// helperRelease names the environment variable with the file whose creation ends a holding grandchild.
const helperRelease = "CAPTURE_TEST_HELPER_RELEASE"

// TestHelperProcess is the child fixture of this package's tests: it writes to both streams and
// exits 3 (`streams`), starts a grandchild that keeps the inherited output open until a release
// file appears (`hold`), waits for the release file itself (`wait`), or reports whether its output
// handles complete writes asynchronously (`handle-modes`). It returns at once in a normal test run.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperMode)
	if mode == "" {
		return
	}
	switch mode {
	case "streams":
		fmt.Fprint(os.Stdout, "answer on stdout")
		fmt.Fprint(os.Stderr, "warning on stderr")
		os.Exit(3)
	case "hold":
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		grandchild := exec.Command(executable, "-test.run=^TestHelperProcess$")
		grandchild.Env = append(os.Environ(), helperMode+"=wait")
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
		if err := grandchild.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		if err := grandchild.Process.Release(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		fmt.Fprint(os.Stdout, "parent done")
		os.Exit(0)
	case "wait":
		waitForRelease()
		os.Exit(0)
	case "handle-modes":
		fmt.Fprint(os.Stdout, handleModes())
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unknown helper mode", mode)
	os.Exit(125)
}

// waitForRelease blocks until the release file exists or ten seconds passed.
func waitForRelease() {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(os.Getenv(helperRelease)); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// helper builds a command that runs this test executable in the given fixture mode.
func helper(ctx context.Context, t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() {
		// Let a grandchild that still holds a pipe exit instead of lingering for its ten seconds.
		if err := os.WriteFile(release, nil, 0600); err != nil {
			t.Log(err)
		}
		time.Sleep(50 * time.Millisecond)
	})
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	command.Env = append(os.Environ(), helperMode+"="+mode, helperRelease+"="+release)
	return command
}

// TestRunSeparatesStreamsAndReportsExitStatus proves Run delivers standard output and standard
// error to their own writers and returns the child's exit status through the test executable's
// child fixture.
func TestRunSeparatesStreamsAndReportsExitStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := capture.Run(helper(context.Background(), t, "streams"), &stdout, &stderr)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v; want exit status 3", err)
	}
	if stdout.String() != "answer on stdout" || stderr.String() != "warning on stderr" {
		t.Fatalf("stdout = %q stderr = %q", stdout.String(), stderr.String())
	}
}

// TestRunDiscardsNilWriters proves Run accepts nil writers for both streams and still reports the
// child's exit status through the test executable's child fixture.
func TestRunDiscardsNilWriters(t *testing.T) {
	err := capture.Run(helper(context.Background(), t, "streams"), nil, nil)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v; want exit status 3", err)
	}
}

// TestRunStopsCopyingAtWaitDelayWhenDescendantHoldsOutput proves Run returns exec.ErrWaitDelay
// within the command's WaitDelay after the child exits while a grandchild keeps the inherited
// output open, and keeps what the child wrote, through the test executable's child fixture.
func TestRunStopsCopyingAtWaitDelayWhenDescendantHoldsOutput(t *testing.T) {
	command := helper(context.Background(), t, "hold")
	command.WaitDelay = 200 * time.Millisecond
	var stdout bytes.Buffer
	start := time.Now()
	err := capture.Run(command, &stdout, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("err = %v after %v; want %v", err, elapsed, exec.ErrWaitDelay)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v; want a return shortly after the 200ms WaitDelay", elapsed)
	}
	if !strings.Contains(stdout.String(), "parent done") {
		t.Fatalf("stdout = %q; want the child's output", stdout.String())
	}
}

// TestRunReturnsWhenContextEndsChild proves Run returns an error soon after the context's deadline
// kills a child that never exits on its own, through the test executable's child fixture.
func TestRunReturnsWhenContextEndsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	command := helper(ctx, t, "wait")
	command.WaitDelay = 200 * time.Millisecond
	start := time.Now()
	err := capture.Run(command, nil, nil)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v; want a prompt failure", err, time.Since(start))
	}
}
