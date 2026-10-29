//go:build windows

package capture

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"

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
	wait, err := startPiped(command, stdout, stderr)
	if err != nil {
		return nil, err
	}
	return func() error { result, _ := wait(); return result }, nil
}

// newCapturePipe creates the pipe under a random name with a single instance that no other
// server can take over (FILE_FLAG_FIRST_PIPE_INSTANCE) and connects the child end to it, so a
// connected child end proves the parent end is this process's own.
func newCapturePipe() (*outputPipe, error) {
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
	return &outputPipe{parent: os.NewFile(uintptr(server), name), child: os.NewFile(uintptr(client), name)}, nil
}
