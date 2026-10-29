package capture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// RunOwned captures an unstarted exec.Command whose local process group/tree belongs to the
// caller. Cancellation terminates that group on Linux or its job on Windows, then drains output
// within WaitDelay (one second by default). Use Run for intentionally detached work. This does
// not cancel operations in a remote engine, nor Linux descendants that deliberately leave the
// process group. A normal exit does not terminate detached descendants; an inherited pipe that exceeds
// WaitDelay returns exec.ErrWaitDelay and terminates the remaining owned group/tree.
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
	wait, err := Start(command, stdout, stderr)
	if err != nil {
		return err
	}
	if err := activate(); err != nil {
		treeErr := terminate()
		killErr := command.Process.Kill()
		return errors.Join(err, treeErr, killErr, wait())
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
	waitErr := wait()
	close(done)
	stopErr := <-stopped
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), waitErr, stopErr)
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		return errors.Join(waitErr, terminate())
	}
	return errors.Join(waitErr, stopErr)
}
