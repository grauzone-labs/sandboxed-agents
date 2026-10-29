// Package capture runs a child process and delivers its standard output and standard error to
// writers through handles on which the child and the descendants that inherit them can complete
// their writes.
//
// On Linux ordinary capture assigns the writers to the command, as exec.Cmd does; owned capture
// uses explicit pipes to observe output-drain expiration independently of the parent's exit error.
// On Windows the streams pass through named pipes opened for overlapped I/O: the inbox Windows OpenSSH client
// waits for the asynchronous completion of each write and blocks for ever on a pipe or file that
// its parent created without FILE_FLAG_OVERLAPPED, as Go's exec and os.Create do (ADR-0008). The
// handles reach only the processes that inherit them: a program in between that creates its own
// pipes for its child, as podman machine ssh does for the client, is not covered, which is why the
// preflight starts the client itself.
package capture

import (
	"io"
	"os/exec"
)

// Run starts command through Start and waits for it.
func Run(command *exec.Cmd, stdout, stderr io.Writer) error {
	wait, err := Start(command, stdout, stderr)
	if err != nil {
		return err
	}
	return wait()
}
