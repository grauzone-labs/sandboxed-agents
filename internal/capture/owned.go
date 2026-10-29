package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"sync"
	"time"
)

// RunOwned captures an unstarted exec.Command whose local process group/tree belongs to the
// caller. Cancellation terminates that group on Linux or its job on Windows, then drains output
// within WaitDelay (one second by default). Use Run for intentionally detached work. This does
// not cancel operations in a remote engine, nor Linux descendants that deliberately leave the
// process group. A normal exit does not terminate detached descendants; an inherited pipe that exceeds
// WaitDelay adds exec.ErrWaitDelay to any parent exit error and terminates the remaining owned tree.
// When stdout and stderr are the same comparable writer, calls to its Write method are serialized.
func RunOwned(ctx context.Context, command *exec.Cmd, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command.Cancel != nil {
		return fmt.Errorf("owned command must use exec.Command; pass cancellation through RunOwned")
	}
	if command.WaitDelay == 0 {
		command.WaitDelay = time.Second
	}
	activate, terminate, release, err := prepareOwned(command)
	if err != nil {
		return err
	}
	defer release()
	if stdout != nil && reflect.ValueOf(stdout).Comparable() && reflect.ValueOf(stderr).Comparable() && stdout == stderr {
		shared := &ownedWriter{writer: stdout}
		stdout, stderr = shared, shared
	}
	wait, err := startPiped(command, stdout, stderr)
	if err != nil {
		return err
	}
	if err := activate(); err != nil {
		treeErr := terminate()
		killErr := command.Process.Kill()
		waitErr, _ := wait()
		return errors.Join(err, treeErr, killErr, waitErr)
	}
	done := make(chan struct{})
	stopped := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			stopped <- terminate()
		case <-done:
			stopped <- nil
		}
	}()
	waitErr, drainExpired := wait()
	close(done)
	stopErr := <-stopped
	if drainExpired || errors.Is(waitErr, exec.ErrWaitDelay) {
		stopErr = errors.Join(stopErr, terminate())
		if !errors.Is(waitErr, exec.ErrWaitDelay) {
			waitErr = errors.Join(waitErr, exec.ErrWaitDelay)
		}
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), waitErr, stopErr)
	}
	return errors.Join(waitErr, stopErr)
}

// ownedWriter serializes both captured streams when they share one destination.
type ownedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

// Write forwards one stream chunk while excluding the other stream's concurrent writes.
func (w *ownedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
