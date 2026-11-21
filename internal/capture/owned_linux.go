package capture

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// prepareOwned places only this invocation in a fresh process group and returns its cancellation
// action. A descendant that deliberately starts a separate session is outside this boundary. When
// TrackOwnedGroups set a directory, activation records the running group there and release removes
// the record.
func prepareOwned(command *exec.Cmd) (activate func() error, terminate func() error, release func(), err error) {
	if command.SysProcAttr != nil {
		return nil, nil, nil, fmt.Errorf("owned command already has process attributes")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	directory := trackedGroupDirectory()
	record := ""
	activate = func() error {
		if directory == "" {
			return nil
		}
		path, err := recordGroup(directory, command.Process.Pid)
		record = path
		if err != nil {
			return fmt.Errorf("record owned process group: %w", err)
		}
		return nil
	}
	release = func() {
		if record != "" {
			_ = os.Remove(record)
		}
	}
	return activate, func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}, release, nil
}

// recordGroup creates the empty PID.STARTTICK record of the group led by pid in directory. It
// returns the record path once the file exists, so a failed close still leaves it removable.
func recordGroup(directory string, pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return "", fmt.Errorf("invalid process metadata")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("invalid process metadata")
	}
	path := filepath.Join(directory, fmt.Sprintf("%d.%s", pid, fields[19]))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	return path, file.Close()
}
