package gitcredentials

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// agentProcess identifies a dedicated agent by PID, boot identity, and kernel start time without storing any secret.
type agentProcess struct {
	PID   int
	Start string
	Boot  string
}

// processStart identifies a same-user process so a stale record can never signal a reused PID.
func processStart(pid int) (string, error) {
	path := "/proc/" + strconv.Itoa(pid)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", os.ErrPermission
	}
	data, err := os.ReadFile(path + "/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", errors.New("cannot inspect agent start time")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", errors.New("cannot inspect agent start time")
	}
	return fields[19], nil
}

// agentIdentity reads the actual daemon PID over Assuan; a listening socket may have been created before the daemon forked.
func agentIdentity(a *agentConnection) (agentProcess, error) {
	data, err := a.command("GETINFO pid", nil)
	if err != nil {
		return agentProcess{}, err
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		return agentProcess{}, errors.New("invalid dedicated agent identity")
	}
	start, err := processStart(pid)
	if err != nil {
		return agentProcess{}, errors.New("cannot verify dedicated agent process")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return agentProcess{}, errors.New("cannot read kernel boot identity")
	}
	return agentProcess{PID: pid, Start: start, Boot: strings.TrimSpace(string(boot))}, nil
}

// rememberAgent records only process identity before any verified passphrase enters agent memory.
func (s Store) rememberAgent(a *agentConnection) error {
	process, err := agentIdentity(a)
	if err != nil {
		return err
	}
	data, err := json.Marshal(process)
	if err != nil {
		return errors.New("cannot encode dedicated agent identity")
	}
	if err := os.WriteFile(filepath.Join(s.directory(), "agent-process.json"), data, 0600); err != nil {
		return errors.New("cannot record dedicated agent identity")
	}
	return nil
}

// terminateProcess signals a verified agent through a pidfd, checking start time after opening the handle to reject PID reuse.
func terminateProcess(process agentProcess) (bool, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return false, errors.New("cannot inspect kernel boot identity")
	}
	if strings.TrimSpace(string(boot)) != process.Boot {
		return false, nil
	}
	fd, err := unix.PidfdOpen(process.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot acquire dedicated agent process handle")
	}
	defer unix.Close(fd)
	start, err := processStart(process.PID)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || err == nil && start != process.Start {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot verify dedicated agent process identity")
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return false, errors.New("cannot relock dedicated Git credential agent")
	}
	count, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(socketDeadline.Milliseconds()))
	if err != nil || count == 0 {
		return false, errors.New("cannot confirm dedicated agent termination")
	}
	return true, nil
}

// terminateAgent uses the recorded daemon identity to relock even when agent IPC is stalled, never signaling a stale PID.
func (s Store) terminateAgent(socket string) error {
	data, err := os.ReadFile(filepath.Join(s.directory(), "agent-process.json"))
	if err == nil {
		var process agentProcess
		if json.Unmarshal(data, &process) != nil || process.PID <= 0 || process.Start == "" || process.Boot == "" {
			return errors.New("dedicated agent identity is damaged")
		}
		stopped, err := terminateProcess(process)
		if err != nil {
			return err
		}
		if stopped {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect dedicated agent identity")
	}
	// Before the first unlock there is no record. A responsive agent may also have
	// been started by GPG after the recorded agent ended; that agent has no product unlock.
	connection, err := net.DialTimeout("unix", socket, socketDeadline)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ECONNREFUSED) {
		return nil
	}
	if err != nil {
		return errors.New("cannot reach dedicated agent for relocking")
	}
	connection.Close()
	agent, err := connectAgent(socket)
	if err != nil {
		return err
	}
	defer agent.Close()
	process, err := agentIdentity(agent)
	if err != nil {
		return err
	}
	_, err = terminateProcess(process)
	return err
}
