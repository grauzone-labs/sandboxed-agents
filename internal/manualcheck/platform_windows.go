//go:build windows

package manualcheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// init binds processElevated to the elevation of this process's token.
func init() {
	processElevated = readProcessElevated
}

// programDataDirectory returns the machine's program data folder through the known-folder API, the
// same folder the elevated observer resolves for its protected output directories.
func programDataDirectory() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
}

// verifyProtectedOutput accepts a capture run's output directory only when it, its output parent, and
// the trusted base are directories that are no reparse point and are owned by Administrators or
// SYSTEM, as the elevated observer creates them; a directory the standard account created or linked
// before any observer ran is refused.
func verifyProtectedOutput(output string) error {
	for _, path := range []string{output, filepath.Dir(output), filepath.Dir(filepath.Dir(output))} {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(name)
		if err != nil {
			return fmt.Errorf("protected output %s: %w", path, err)
		}
		if attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("protected output %s is a link or not a directory", path)
		}
		descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			return fmt.Errorf("protected output %s: %w", path, err)
		}
		owner, _, err := descriptor.Owner()
		if err != nil {
			return fmt.Errorf("protected output %s: %w", path, err)
		}
		if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid) {
			return fmt.Errorf("protected output %s is not owned by Administrators or SYSTEM; it was not created by the observer", path)
		}
	}
	return nil
}

// readProcessElevated reads TokenElevation of the current process token. Unlike Token.IsElevated,
// which reports false when the query fails, it returns the error, so the caller refuses.
func readProcessElevated() (bool, error) {
	var elevation, size uint32
	token := windows.GetCurrentProcessToken()
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &size); err != nil {
		return false, err
	}
	if size != uint32(unsafe.Sizeof(elevation)) {
		return false, fmt.Errorf("TokenElevation returned %d bytes", size)
	}
	return elevation != 0, nil
}

// childEcho builds the child process that proves the controller group is exported: a fresh
// PowerShell printing the variable.
func childEcho(group string) *exec.Cmd {
	command := exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", `"group: $env:SANDBOXED_AGENTS_GROUP"`)
	command.Env = append(os.Environ(), "SANDBOXED_AGENTS_GROUP="+group)
	return command
}

// freeBytes returns the free space of the volume that holds path.
func freeBytes(path string) (uint64, error) {
	var free, total, available uint64
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(name, &free, &total, &available); err != nil {
		return 0, err
	}
	return free, nil
}

// processIdentity returns the creation time of a process, for the evidence of which process was
// killed; the kill itself goes through the process handle Go holds.
func processIdentity(pid int) string {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "unavailable"
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("created_utc=%s", time.Unix(0, creation.Nanoseconds()).UTC().Format(time.RFC3339Nano))
}

// hostDiagnostics returns platform-specific, secret-free host facts for the preflight.
func hostDiagnostics() []*exec.Cmd {
	return []*exec.Cmd{
		exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", "[Environment]::OSVersion.VersionString"),
		exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", "[Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)"),
		exec.Command("podman", "machine", "list", "--format", "{{.Name}} {{.Default}} {{.Running}}"),
		exec.Command("wsl", "--status"),
	}
}

// nullInput is the file read as "no standard input".
const nullInput = "NUL"
