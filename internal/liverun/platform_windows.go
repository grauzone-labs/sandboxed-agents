package liverun

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// freeSpace reads per-user available bytes through GetDiskFreeSpaceEx without administrator rights.
func freeSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(p, &available, &total, &free)
	return available, err
}

// lockFile takes an exclusive nonblocking kernel lock that is released when its owner exits.
func lockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// detachCommand isolates the supervisor from the launching console without requiring elevation.
func detachCommand(cmd *exec.Cmd, policy SessionPolicy) {
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	if policy.InJob && policy.Breakaway && !policy.SilentBreakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}

// processIdentity returns a live process's creation timestamp without administrator access.
func processIdentity(pid int) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(process)
	var code uint32
	if err = windows.GetExitCodeProcess(process, &code); err != nil {
		return "", err
	}
	if code != 259 {
		return "", fmt.Errorf("process ended")
	}
	var creation, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(process, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", creation.HighDateTime, creation.LowDateTime), nil
}

// machineDisk locates the selected WSL distribution's actual VHDX through its per-user registry.
func machineDisk(name string) (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, "Software\\Microsoft\\Windows\\CurrentVersion\\Lxss", registry.READ)
	if err != nil {
		return "", err
	}
	defer key.Close()
	children, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return "", err
	}
	found := ""
	for _, child := range children {
		distro, err := registry.OpenKey(key, child, registry.READ)
		if err != nil {
			return "", err
		}
		distribution, _, nameErr := distro.GetStringValue("DistributionName")
		base, _, baseErr := distro.GetStringValue("BasePath")
		distro.Close()
		if nameErr != nil || distribution != name {
			continue
		}
		if baseErr != nil || !windowsAbsolute(base) {
			return "", fmt.Errorf("WSL virtual disk base path is unavailable")
		}
		disk, err := virtualDisk(base)
		if err != nil {
			return "", err
		}
		if found != "" {
			return "", fmt.Errorf("selected WSL distribution is ambiguous")
		}
		found = disk
	}
	if found == "" {
		return "", fmt.Errorf("selected WSL distribution is unavailable")
	}
	return found, nil
}

// virtualDisk returns the one regular .vhdx file in base, reading base as a
// literal directory so pattern characters in its name are never expanded.
func virtualDisk(base string) (string, error) {
	entries, err := os.ReadDir(base)
	disks := []string{}
	for _, entry := range entries {
		if matched, _ := filepath.Match("*.vhdx", entry.Name()); matched {
			disks = append(disks, filepath.Join(base, entry.Name()))
		}
	}
	if err != nil || len(disks) != 1 {
		return "", fmt.Errorf("selected WSL virtual disk is ambiguous or missing")
	}
	info, err := os.Stat(disks[0])
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("selected WSL virtual disk is unreadable")
	}
	return disks[0], nil
}

// prepareSuite makes the owned suite tree die if its detached supervisor is killed.
func prepareSuite(cmd *exec.Cmd, _ *os.File) (func() error, func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		windows.CloseHandle(job)
		return nil, nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	activate := func() error {
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return err
		}
		defer windows.CloseHandle(process)
		if err = windows.AssignProcessToJobObject(job, process); err != nil {
			return err
		}
		snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(snapshot)
		entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
		err = windows.Thread32First(snapshot, &entry)
		for err == nil {
			if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
				thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
				if err != nil {
					return err
				}
				count, resumeErr := windows.ResumeThread(thread)
				windows.CloseHandle(thread)
				if resumeErr != nil {
					return resumeErr
				}
				if count != 1 {
					return fmt.Errorf("unexpected suite suspend count")
				}
				return nil
			}
			err = windows.Thread32Next(snapshot, &entry)
		}
		return fmt.Errorf("suite thread unavailable: %w", err)
	}
	closed := false
	return activate, func() {
		if !closed {
			windows.CloseHandle(job)
			closed = true
		}
	}, nil
}

// suiteEndsWithSupervisor is true on Windows: the kill-on-close job ends the suite when its supervisor dies.
const suiteEndsWithSupervisor = true

// suiteAlive reports the owned go process; a dead supervisor closes its kill-on-close suite job.
func suiteAlive(ref processRef) bool { return ref.live() }

// abortSuite stops the owned go process; closing its prepared job stops every descendant.
func abortSuite(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// isProcessInJob calls the documented membership query absent from the x/sys wrapper surface.
var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// sessionJobPolicy reads the current process's immediate job limits without administrator rights.
func sessionJobPolicy() (SessionPolicy, error) {
	var member int32
	ok, _, err := isProcessInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&member)))
	if ok == 0 {
		return SessionPolicy{}, fmt.Errorf("query job membership: %w", err)
	}
	p := SessionPolicy{InJob: member != 0}
	if !p.InJob {
		return p, nil
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		return p, err
	}
	flags := limits.BasicLimitInformation.LimitFlags
	p.KillOnClose = flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0
	p.Breakaway = flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0
	p.SilentBreakaway = flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0
	return p, nil
}

// replaceBusy reports a replacement refused because a reader holds the
// destination open without delete sharing, as os.ReadFile does; the handle
// closes after the read, so publication can retry.
func replaceBusy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
