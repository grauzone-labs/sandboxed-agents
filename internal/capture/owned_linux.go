package capture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// ownedRecordHook, when set by a test, runs with the started process ID just before its group is recorded.
var ownedRecordHook func(pid int)

// The start gate's inherited release and status descriptors and the command path it executes once released.
const (
	gateFDEnv     = "SANDBOXED_AGENTS_OWNED_GATE_FD"
	gateStatusEnv = "SANDBOXED_AGENTS_OWNED_GATE_STATUS_FD"
	gatePathEnv   = "SANDBOXED_AGENTS_OWNED_GATE_PATH"
)

// init turns a process started as a tracked command's start gate into that command.
func init() {
	if fd := os.Getenv(gateFDEnv); fd != "" {
		runStartGate(fd, os.Getenv(gateStatusEnv), os.Getenv(gatePathEnv))
	}
}

// runStartGate waits for the caller's release byte on the release descriptor and then executes path
// with this process's argv and environment, the gate variables removed. The caller writes the byte
// only once the group is recorded, so a caller that dies first closes the pipe and the command never
// runs. The status descriptor closes on a successful exec; a refused exec writes its errno there
// instead, so the caller can return the error Start would have returned. It never returns.
func runStartGate(releaseText, statusText, path string) {
	release, releaseErr := strconv.Atoi(releaseText)
	status, statusErr := strconv.Atoi(statusText)
	if releaseErr != nil || statusErr != nil || path == "" {
		fmt.Fprintln(os.Stderr, "owned start gate misconfigured")
		os.Exit(125)
	}
	var released [1]byte
	n, err := syscall.Read(release, released[:])
	for errors.Is(err, syscall.EINTR) {
		n, err = syscall.Read(release, released[:])
	}
	_ = syscall.Close(release)
	if n != 1 {
		os.Exit(125)
	}
	syscall.CloseOnExec(status)
	environment := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, gateFDEnv+"=") || strings.HasPrefix(entry, gateStatusEnv+"=") || strings.HasPrefix(entry, gatePathEnv+"=")
	})
	err = syscall.Exec(path, os.Args, environment)
	errno, ok := err.(syscall.Errno)
	if !ok {
		errno = syscall.EINVAL
	}
	var payload [4]byte
	binary.LittleEndian.PutUint32(payload[:], uint32(errno))
	_, _ = syscall.Write(status, payload[:])
	os.Exit(127)
}

// prepareOwned places only this invocation in a fresh process group and returns its cancellation
// action. A descendant that deliberately starts a separate session is outside this boundary.
//
// When TrackOwnedGroups set a directory, every command first starts as a gate: this executable,
// through /proc/self/exe, holding the read end of a release pipe and the write end of a status pipe.
// Activation records the gate's group, which the command keeps since exec preserves the PID and its
// start tick, and only then writes the byte that lets the gate execute the command with the caller's
// argv, environment, standard streams and directory. The group is therefore recorded before the
// command can run, and a caller that dies before recording leaves a gate that exits on end of file.
// Activation then waits for the status pipe: end of file means the exec succeeded, and an errno means
// it was refused, which activation returns as the *os.PathError Start returns for that refusal.
// Release removes the record and restores the command's fields.
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
	if directory == "" {
		return func() error { return nil }, terminate, func() {}, nil
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create owned start gate: %w", err)
	}
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		gateRead.Close()
		gateWrite.Close()
		return nil, nil, nil, fmt.Errorf("create owned start gate: %w", err)
	}
	path, environment, extra := command.Path, command.Env, command.ExtraFiles
	command.Env = append(command.Environ(), gateFDEnv+"="+strconv.Itoa(3+len(extra)), gateStatusEnv+"="+strconv.Itoa(4+len(extra)), gatePathEnv+"="+path)
	command.ExtraFiles = append(slices.Clip(extra), gateRead, statusWrite)
	command.Path = "/proc/self/exe"
	record := ""
	restored := false
	restore := func() {
		if !restored {
			restored = true
			command.Path, command.Env, command.ExtraFiles = path, environment, extra
			gateRead.Close()
			statusWrite.Close()
		}
	}
	activate = func() error {
		restore()
		defer statusRead.Close()
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
		gateWrite.Close()
		var payload [4]byte
		n, err := io.ReadFull(statusRead, payload[:])
		switch {
		case n == 0 && errors.Is(err, io.EOF):
			return nil
		case n == len(payload):
			return &startRefused{err: &os.PathError{Op: "fork/exec", Path: path, Err: syscall.Errno(binary.LittleEndian.Uint32(payload[:]))}}
		}
		return fmt.Errorf("owned start gate status unverified: %w", err)
	}
	release = func() {
		restore()
		gateWrite.Close()
		statusRead.Close()
		if record != "" {
			_ = os.Remove(record)
		}
	}
	return activate, terminate, release, nil
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
