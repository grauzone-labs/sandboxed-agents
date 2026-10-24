//go:build !windows

package capture

import (
	"io"
	"os/exec"
)

// Start starts command with its standard output and standard error delivered to stdout and
// stderr, as exec.Cmd.Start does with those writers assigned, and returns the wait for its end; a
// nil writer discards its stream. After the process exits, the wait stops copying at
// command.WaitDelay when a descendant keeps a pipe open and reports exec.ErrWaitDelay unless the
// process itself failed.
func Start(command *exec.Cmd, stdout, stderr io.Writer) (func() error, error) {
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command.Wait, nil
}
