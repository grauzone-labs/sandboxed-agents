// Package capture runs a child process and delivers its standard output and standard error to
// writers through handles on which every descendant can complete its writes.
//
// On Linux the package assigns the writers to the command, as exec.Cmd does. On Windows the
// streams pass through named pipes opened for overlapped I/O: the inbox Windows OpenSSH client,
// which podman machine ssh starts with the handles it inherited, waits for the asynchronous
// completion of each write and blocks for ever on a pipe or file that the parent created without
// FILE_FLAG_OVERLAPPED, as Go's exec and os.Create do (ADR-0008).
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
