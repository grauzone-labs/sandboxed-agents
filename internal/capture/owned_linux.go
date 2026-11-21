package capture

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ownedRecordHook, when set by a test, runs with the started process ID just before its group is recorded.
var ownedRecordHook func(pid int)

// The start gate's inherited descriptor and the command path it executes once released.
const (
	gateFDEnv   = "SANDBOXED_AGENTS_OWNED_GATE_FD"
	gatePathEnv = "SANDBOXED_AGENTS_OWNED_GATE_PATH"
)

// init turns a process started as a tracked command's start gate into that command.
func init() {
	if fd := os.Getenv(gateFDEnv); fd != "" {
		runStartGate(fd, os.Getenv(gatePathEnv))
	}
}

// runStartGate waits for the caller's release byte on fd and then executes path with this process's
// argv and environment, the gate variables removed. The caller writes the byte only once the group is
// recorded, so a caller that dies first closes the pipe and the command never runs. It never returns.
func runStartGate(fdText, path string) {
	fd, err := strconv.Atoi(fdText)
	if err != nil || path == "" {
		fmt.Fprintln(os.Stderr, "owned start gate misconfigured")
		os.Exit(125)
	}
	var released [1]byte
	n, err := syscall.Read(fd, released[:])
	for errors.Is(err, syscall.EINTR) {
		n, err = syscall.Read(fd, released[:])
	}
	_ = syscall.Close(fd)
	if n != 1 {
		os.Exit(125)
	}
	environment := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, gateFDEnv+"=") || strings.HasPrefix(entry, gatePathEnv+"=")
	})
	err = syscall.Exec(path, os.Args, environment)
	fmt.Fprintf(os.Stderr, "exec %s: %v\n", path, err)
	os.Exit(127)
}

// prepareOwned places only this invocation in a fresh process group and returns its cancellation
// action. A descendant that deliberately starts a separate session is outside this boundary.
//
// When TrackOwnedGroups set a directory, the command first starts as a gate: this executable, through
// /proc/self/exe, holding the read end of a pipe. Activation records the gate's group, which the
// command keeps since exec preserves the PID and its start tick, and only then writes the byte that
// lets the gate execute the command with the caller's argv, environment, standard streams and
// directory. The group is therefore recorded before the command can run, and a caller that dies
// before recording leaves a gate that exits on end of file. Release removes the record and restores
// the command's fields.
func prepareOwned(command *exec.Cmd) (activate func() error, terminate func() error, release func(), err error) {
	if command.SysProcAttr != nil {
		return nil, nil, nil, fmt.Errorf("owned command already has process attributes")
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	terminate = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	directory := trackedGroupDirectory()
	if directory == "" || !gateable(command) {
		return func() error { return nil }, terminate, func() {}, nil
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create owned start gate: %w", err)
	}
	path, environment, extra := command.Path, command.Env, command.ExtraFiles
	command.Env = append(command.Environ(), gateFDEnv+"="+strconv.Itoa(3+len(extra)), gatePathEnv+"="+path)
	command.ExtraFiles = append(slices.Clip(extra), gateRead)
	command.Path = "/proc/self/exe"
	record := ""
	restored := false
	restore := func() {
		if !restored {
			restored = true
			command.Path, command.Env, command.ExtraFiles = path, environment, extra
			gateRead.Close()
		}
	}
	activate = func() error {
		restore()
		defer gateWrite.Close()
		if ownedRecordHook != nil {
			ownedRecordHook(command.Process.Pid)
		}
		recorded, err := recordGroup(directory, command.Process.Pid)
		record = recorded
		if err != nil {
			return fmt.Errorf("record owned process group: %w", err)
		}
		if _, err := gateWrite.Write([]byte{1}); err != nil {
			return fmt.Errorf("release owned start gate: %w", err)
		}
		return nil
	}
	release = func() {
		restore()
		gateWrite.Close()
		if record != "" {
			_ = os.Remove(record)
		}
	}
	return activate, terminate, release, nil
}

// gateable reports whether command can start behind the gate with unchanged start errors: its
// lookup succeeded and its path names an executable regular file. Otherwise it starts directly, so
// Start reports the failure and nothing runs to record. A file that passes these checks but that
// exec still refuses, such as one in an unknown format, exits with status 127 and a message on
// standard error instead of failing Start.
func gateable(command *exec.Cmd) bool {
	if command.Err != nil {
		return false
	}
	path := command.Path
	if !filepath.IsAbs(path) && command.Dir != "" {
		path = filepath.Join(command.Dir, path)
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && unix.Access(path, unix.X_OK) == nil
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
