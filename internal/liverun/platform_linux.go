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

// suiteAlive also observes surviving test descendants in the original owned process group.
func suiteAlive(ref processRef) bool {
	if ref.PID <= 0 {
		return false
	}
	if ref.live() {
		return true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		end := strings.LastIndex(string(data), ")")
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) > 2 && fields[0] != "Z" && fields[0] != "X" && fields[2] == strconv.Itoa(ref.PID) {
			return true
		}
	}
	return false
}

// abortSuite terminates only the process group created for this owned suite.
func abortSuite(cmd *exec.Cmd) error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

// sessionJobPolicy reports no Windows job for a Linux process.
func sessionJobPolicy() (SessionPolicy, error) { return SessionPolicy{}, nil }
