package liverun

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// freeSpace reads the calling user's available filesystem blocks.
func freeSpace(path string) (uint64, error) {
	var stat unix.Statfs_t
	err := unix.Statfs(path, &stat)
	return availableBytes(stat), err
}

// availableBytes converts available blocks to bytes in the filesystem's fragment
// size, the unit statvfs counts blocks in, and uses the block size when the
// filesystem reports no fragment size.
func availableBytes(stat unix.Statfs_t) uint64 {
	unit := uint64(stat.Frsize)
	if unit == 0 {
		unit = uint64(stat.Bsize)
	}
	return stat.Bavail * unit
}

// lockFile takes an exclusive nonblocking kernel lock.
func lockFile(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

// detachCommand isolates the supervisor from the launching SSH terminal.
func detachCommand(cmd *exec.Cmd, _ SessionPolicy) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processIdentity returns the Linux process start tick, excluding dead and zombie processes.
func processIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return "", fmt.Errorf("invalid process metadata")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", fmt.Errorf("process ended")
	}
	return fields[19], nil
}

// machineDisk is unused on Linux, whose engine storage is local.
func machineDisk(string) (string, error) { return "", fmt.Errorf("Linux has no Podman machine") }

// prepareSuite isolates the suite's process group and keeps the engine lock in its go process.
func prepareSuite(cmd *exec.Cmd, lock *os.File) (func() error, func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = append(cmd.ExtraFiles, lock)
	return func() error { return nil }, func() {}, nil
}

// suiteEndsWithSupervisor is false on Linux: the suite runs in its own process group and can outlive a killed supervisor.
const suiteEndsWithSupervisor = false

// suiteAlive also observes surviving test descendants in the original owned process group.
func suiteAlive(ref processRef) bool { return groupAlive("/proc", ref) }

// procStat holds the /proc/PID/stat fields that bind a process to an owned group.
type procStat struct {
	state string
	pgrp  int
	start string
}

// readProcStat parses state, process group and start tick from one /proc stat file under proc.
func readProcStat(proc, pid string) (procStat, error) {
	data, err := os.ReadFile(filepath.Join(proc, pid, "stat"))
	if err != nil {
		return procStat{}, err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return procStat{}, fmt.Errorf("invalid process metadata")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return procStat{}, fmt.Errorf("invalid process metadata")
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return procStat{}, fmt.Errorf("invalid process metadata")
	}
	return procStat{state: fields[0], pgrp: pgrp, start: fields[19]}, nil
}

// ended reports a dead or zombie process, which runs nothing.
func (p procStat) ended() bool { return p.state == "Z" || p.state == "X" }

// groupAlive reports whether the recorded suite leader, or a live member of its
// original process group, remains in the process table under proc. An
// unreadable table counts as alive.
//
// The kernel frees a PID only once no process uses it as a process group ID, so
// a different process now holding the leader's PID proves the original group
// ended and the group ID was reused. Members never predate their group's
// leader, so an earlier start tick also rules a process out. A reused group
// whose new leader already exited cannot be told apart and still counts as alive.
func groupAlive(proc string, ref processRef) bool {
	if ref.PID <= 0 {
		return false
	}
	group := strconv.Itoa(ref.PID)
	leaderStart, recorded := uint64(0), false
	if ref.Identity != "" {
		leaderStart, recorded = parseTick(ref.Identity)
	}
	if leader, err := readProcStat(proc, group); err == nil && recorded {
		if leader.start != ref.Identity {
			return false
		}
		if !leader.ended() {
			return true
		}
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		member, err := readProcStat(proc, entry.Name())
		if err != nil || member.ended() || member.pgrp != ref.PID {
			continue
		}
		if start, ok := parseTick(member.start); recorded && ok && start < leaderStart {
			continue
		}
		return true
	}
	return false
}

// parseTick reads a /proc start tick, reporting whether it is a valid number.
func parseTick(value string) (uint64, bool) {
	tick, err := strconv.ParseUint(value, 10, 64)
	return tick, err == nil
}

// abortSuite terminates only the process group created for this owned suite.
func abortSuite(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// sessionJobPolicy reports no Windows job for a Linux process.
func sessionJobPolicy() (SessionPolicy, error) { return SessionPolicy{}, nil }

// replaceBusy reports no transient replacement failure: Linux renames over open files.
func replaceBusy(error) bool { return false }
