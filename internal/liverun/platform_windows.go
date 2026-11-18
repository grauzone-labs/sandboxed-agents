package liverun

import (
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
func detachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
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
		disks, err := filepath.Glob(filepath.Join(base, "*.vhdx"))
		if err != nil || len(disks) != 1 {
			return "", fmt.Errorf("selected WSL virtual disk is ambiguous or missing")
		}
		info, err := os.Stat(disks[0])
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("selected WSL virtual disk is unreadable")
		}
		if found != "" {
			return "", fmt.Errorf("selected WSL distribution is ambiguous")
		}
		found = disks[0]
	}
	if found == "" {
		return "", fmt.Errorf("selected WSL distribution is unavailable")
	}
	return found, nil
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

// suiteAlive reports the owned go process; a dead supervisor closes its kill-on-close suite job.
func suiteAlive(ref processRef) bool { return ref.live() }

// abortSuite stops the owned go process; closing its prepared job stops every descendant.
func abortSuite(cmd *exec.Cmd) error { return cmd.Process.Kill() }
