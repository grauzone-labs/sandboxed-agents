package capture

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// startPiped captures output through explicit files so process waiting cannot hide output-drain
// expiration. Its wait reports the usual process-first error and, independently, whether either
// output pipe exceeded WaitDelay. Ordinary capture may ignore that fact; owned capture must not.
func startPiped(command *exec.Cmd, stdout, stderr io.Writer) (func() (error, bool), error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outPipe, err := newCapturePipe()
	if err != nil {
		return nil, fmt.Errorf("capture standard output: %w", err)
	}
	errPipe, err := newCapturePipe()
	if err != nil {
		outPipe.close()
		return nil, fmt.Errorf("capture standard error: %w", err)
	}
	command.Stdout, command.Stderr = outPipe.child, errPipe.child
	if err := command.Start(); err != nil {
		outPipe.close()
		errPipe.close()
		return nil, err
	}
	// The child holds inheritable duplicates; closing the originals lets the reads end with the
	// last writer.
	outPipe.child.Close()
	errPipe.child.Close()
	var copyErrs [2]error
	copied := make(chan struct{})
	go func() {
		var group sync.WaitGroup
		group.Add(2)
		go copyStream(stdout, outPipe.parent, &copyErrs[0], &group)
		go copyStream(stderr, errPipe.parent, &copyErrs[1], &group)
		group.Wait()
		close(copied)
	}()
	return func() (error, bool) {
		waitErr := command.Wait()
		cut := false
		if command.WaitDelay > 0 {
			timer := time.NewTimer(command.WaitDelay)
			select {
			case <-copied:
			case <-timer.C:
				// A descendant still holds a pipe: cancel the pending reads.
				cut = true
				outPipe.parent.Close()
				errPipe.parent.Close()
			}
			timer.Stop()
		}
		<-copied
		outPipe.parent.Close()
		errPipe.parent.Close()
		switch {
		case waitErr != nil:
			return waitErr, cut
		case cut:
			return exec.ErrWaitDelay, true
		case copyErrs[0] != nil:
			return copyErrs[0], false
		default:
			return copyErrs[1], false
		}
	}, nil
}

// copyStream copies a pipe into its writer and records the copy's error; a read cut by the wait
// delay is not an error of the copy, the caller reports it.
func copyStream(dst io.Writer, src *os.File, result *error, group *sync.WaitGroup) {
	defer group.Done()
	_, err := io.Copy(dst, src)
	if err != nil && !errors.Is(err, os.ErrClosed) {
		*result = err
	}
}

// outputPipe holds the parent read end and the inheritable child write end of one captured stream.
type outputPipe struct {
	parent, child *os.File
}

// close releases both ends of the captured stream.
func (p *outputPipe) close() {
	p.child.Close()
	p.parent.Close()
}
