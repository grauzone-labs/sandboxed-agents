package gitcredentials

import (
	"errors"
	"golang.org/x/sys/unix"
	"os/exec"
	"syscall"
	"time"
)

// runCommand keeps the group leader unreaped until descendant cleanup, preventing process-group ID reuse during grace.
func runCommand(cmd *exec.Cmd, deadline time.Time) error {
	if deadline.IsZero() {
		deadline = time.Now().Add(HelperDeadline)
	}
	if !time.Now().Before(deadline) {
		return errHelperDeadline
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	var waitErr error
	for {
		var info unix.Siginfo
		waitErr = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if waitErr == unix.EINTR {
			continue
		}
		if waitErr != nil || info.Signo != 0 {
			break
		}
		if time.Now().After(deadline) {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			time.Sleep(KillGrace)
			waitErr = errHelperDeadline
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	remaining := time.Until(deadline.Add(KillGrace))
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	cmd.WaitDelay = remaining
	return errors.Join(waitErr, cmd.Wait())
}
