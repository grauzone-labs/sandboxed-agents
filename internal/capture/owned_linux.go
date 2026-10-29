package capture

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// prepareOwned places only this invocation in a fresh process group and returns its cancellation
// action. A descendant that deliberately starts a separate session is outside this boundary.
func prepareOwned(command *exec.Cmd) (activate func() error, terminate func() error, release func(), err error) {
	if command.SysProcAttr != nil {
		return nil, nil, nil, fmt.Errorf("owned command already has process attributes")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() error { return nil }, func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}, func() {}, nil
}
