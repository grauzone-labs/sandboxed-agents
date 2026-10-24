//go:build windows

package capture

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// Start starts command with its standard output and standard error delivered to stdout and
// stderr and returns the wait for its end; a nil writer discards its stream. Both streams pass
// through named pipes opened for overlapped I/O, so the child, such as the inbox Windows OpenSSH
// client that the preflight starts for its machine queries, completes its writes instead of
// blocking on a synchronous handle (ADR-0008). After the process exits, the wait stops copying at
// command.WaitDelay when a descendant keeps a pipe open; a failure of the process itself is
// reported first, otherwise exec.ErrWaitDelay reports the cut.
func Start(command *exec.Cmd, stdout, stderr io.Writer) (func() error, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outPipe, err := newOverlappedPipe()
	if err != nil {
		return nil, fmt.Errorf("capture standard output: %w", err)
	}
	errPipe, err := newOverlappedPipe()
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
	return func() error {
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
			return waitErr
		case cut:
			return exec.ErrWaitDelay
		case copyErrs[0] != nil:
			return copyErrs[0]
		default:
			return copyErrs[1]
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

// overlappedPipe is a one-instance named pipe whose both ends are opened for overlapped I/O: the
// parent reads, the child writes.
type overlappedPipe struct {
	parent, child *os.File
}

// newOverlappedPipe creates the pipe under a random name with a single instance that no other
// server can take over (FILE_FLAG_FIRST_PIPE_INSTANCE) and connects the child end to it, so a
// connected child end proves the parent end is this process's own.
func newOverlappedPipe() (*overlappedPipe, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := fmt.Sprintf(`\\.\pipe\sandboxed-agents-%d-%s`, windows.GetCurrentProcessId(), hex.EncodeToString(random[:]))
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	server, err := windows.CreateNamedPipe(namePointer,
		windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("create named pipe: %w", err)
	}
	client, err := windows.CreateFile(namePointer, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		windows.CloseHandle(server)
		return nil, fmt.Errorf("open named pipe: %w", err)
	}
	return &overlappedPipe{parent: os.NewFile(uintptr(server), name), child: os.NewFile(uintptr(client), name)}, nil
}

// close releases both ends.
func (p *overlappedPipe) close() {
	p.child.Close()
	p.parent.Close()
}
