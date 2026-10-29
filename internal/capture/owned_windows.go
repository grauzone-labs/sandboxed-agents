package capture

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareOwned starts the child suspended so it cannot create descendants before assignment to
// the invocation's job. Normal completion closes the job without killing persistent processes;
// cancellation terminates its members. No breakaway or host-wide job policy is enabled.
func prepareOwned(command *exec.Cmd) (activate func() error, terminate func() error, release func(), err error) {
	if command.SysProcAttr != nil {
		return nil, nil, nil, fmt.Errorf("owned command already has process attributes")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create owned process job: %w", err)
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	activate = func() error {
		process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended owned process: %w", err)
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return fmt.Errorf("assign owned process job: %w", err)
		}
		return resumeOwnedProcess(uint32(command.Process.Pid))
	}
	return activate, func() error { return windows.TerminateJobObject(job, 1) }, func() { windows.CloseHandle(job) }, nil
}

// resumeOwnedProcess resumes the sole initial thread of the CREATE_SUSPENDED child after job
// assignment. Enumeration and access failures refuse execution rather than permitting a race.
func resumeOwnedProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		count, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		if resumeErr != nil {
			return resumeErr
		}
		if count != 1 {
			return fmt.Errorf("unexpected owned thread suspend count %d", count)
		}
		return nil
	}
	return fmt.Errorf("find suspended owned process thread: %w", err)
}
