package liverun

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	state   string
	pgrp    int
	session int
	start   string
}

// readProcStat parses state, process group, session and start tick from one /proc stat file under proc.
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
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return procStat{}, fmt.Errorf("invalid process metadata")
	}
	return procStat{state: fields[0], pgrp: pgrp, session: session, start: fields[19]}, nil
}

// ended reports a dead or zombie process, which runs nothing.
func (p procStat) ended() bool { return p.state == "Z" || p.state == "X" }

// anySession lets groupState accept a group in every session.
const anySession = -1

// groupAlive reports whether the recorded suite leader, or a live member of its
// original process group, remains in the process table under proc. An
// unreadable table counts as alive.
func groupAlive(proc string, ref processRef) bool {
	alive, known := groupState(proc, ref, anySession)
	return alive || !known
}

// ownedGroupRunning reports whether the group recorded by ref, which must carry
// its leader's start tick, still runs in session according to the process table
// under proc. An unreadable table counts as ended, so nothing unverified is killed.
func ownedGroupRunning(proc string, ref processRef, session int) bool {
	if _, ok := parseTick(ref.Identity); !ok {
		return false
	}
	alive, known := groupState(proc, ref, session)
	return alive && known
}

// groupState reports whether the recorded leader, or a live member of its
// original process group, remains in session (anySession accepts all) in the
// process table under proc, and whether that table could be read.
//
// The kernel frees a PID only once no process uses it as a process group ID, so
// a different process now holding the leader's PID proves the original group
// ended and the group ID was reused. Members never predate their group's
// leader, so an earlier start tick also rules a process out. A reused group
// whose new leader already exited cannot be told apart and still counts as alive.
func groupState(proc string, ref processRef, session int) (alive, known bool) {
	if ref.PID <= 0 {
		return false, true
	}
	group := strconv.Itoa(ref.PID)
	leaderStart, recorded := uint64(0), false
	if ref.Identity != "" {
		leaderStart, recorded = parseTick(ref.Identity)
	}
	inSession := func(p procStat) bool { return session == anySession || p.session == session }
	if leader, err := readProcStat(proc, group); err == nil && recorded {
		if leader.start != ref.Identity {
			return false, true
		}
		if !leader.ended() {
			return inSession(leader), true
		}
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		member, err := readProcStat(proc, entry.Name())
		if err != nil || member.ended() || member.pgrp != ref.PID || !inSession(member) {
			continue
		}
		if start, ok := parseTick(member.start); recorded && ok && start < leaderStart {
			continue
		}
		return true, true
	}
	return false, true
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

// prepareOwnedGroups creates the private run-directory folder where the suite
// records its running owned invocation groups, and returns its path.
func prepareOwnedGroups(directory string) (string, error) {
	path := filepath.Join(directory, "owned-groups")
	return path, os.MkdirAll(path, 0700)
}

// ownedGroupGrace bounds how long abortOwnedGroups waits for killed groups to end.
const ownedGroupGrace = 5 * time.Second

// abortOwnedGroups kills each owned invocation group recorded in directory whose
// leader identity still matches and which still runs in the supervisor's session,
// then waits up to ownedGroupGrace for those groups to end. The suite removes a
// record when its invocation returns, so only invocations a dying suite left
// running remain. Records are PID.STARTTICK files written by capture.RunOwned.
func abortOwnedGroups(directory string) {
	if directory == "" {
		return
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	session, err := unix.Getsid(0)
	if err != nil {
		return
	}
	var killed []processRef
	for _, entry := range entries {
		pid, tick, ok := strings.Cut(entry.Name(), ".")
		leader, err := strconv.Atoi(pid)
		if !ok || err != nil {
			continue
		}
		ref := processRef{PID: leader, Identity: tick}
		if ownedGroupRunning("/proc", ref, session) && syscall.Kill(-leader, syscall.SIGKILL) == nil {
			killed = append(killed, ref)
		}
	}
	deadline := time.Now().Add(ownedGroupGrace)
	for len(killed) > 0 {
		killed = slices.DeleteFunc(killed, func(ref processRef) bool { return !ownedGroupRunning("/proc", ref, session) })
		if len(killed) == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
