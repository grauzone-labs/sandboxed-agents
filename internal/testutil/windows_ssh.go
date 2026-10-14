//go:build windows

package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type WindowsSSHPermissions struct {
	Descriptor string
	Control    windows.SECURITY_DESCRIPTOR_CONTROL
}

func NativeWindowsOpenSSH(t *testing.T) (ssh, keygen string) {
	t.Helper()
	var directories []string
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		directories = append(directories, filepath.Join(systemRoot, "System32", "OpenSSH"))
	}
	if keygen, err := exec.LookPath("ssh-keygen.exe"); err == nil {
		directories = append(directories, filepath.Dir(keygen))
	}
	for _, directory := range directories {
		ssh := filepath.Join(directory, "ssh.exe")
		keygen := filepath.Join(directory, "ssh-keygen.exe")
		if _, err := os.Stat(keygen); err != nil {
			continue
		}
		output, err := RunWindowsSSH(t, ssh, "-V")
		if err == nil && strings.Contains(string(output), "OpenSSH_for_Windows") {
			return ssh, keygen
		}
	}
	t.Skip("native Windows OpenSSH executables are unavailable")
	return "", ""
}

func RunWindowsSSH(t *testing.T, executable string, arguments ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, arguments...).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("OpenSSH command timed out: %v", ctx.Err())
	}
	return output, err
}

func SnapshotWindowsSSHPermissions(t *testing.T, path string) WindowsSSHPermissions {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		t.Fatalf("read Windows permissions for %q: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	return WindowsSSHPermissions{Descriptor: descriptor.String(), Control: control}
}

func AssertWindowsSSHPermissionsUnchanged(t *testing.T, path string, before WindowsSSHPermissions) {
	t.Helper()
	if after := SnapshotWindowsSSHPermissions(t, path); after != before {
		t.Fatalf("Windows permissions on %q changed: before=%+v after=%+v", path, before, after)
	}
}

func AssertWindowsSSHOwnerIsProcessUser(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		t.Fatalf("read Windows owner for %q: %v", path, err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatalf("Windows owner for %q = %v; want process user %v", path, owner, user.User.Sid)
	}
}

func AssertWindowsSSHPathAccess(t *testing.T, path string, protected bool, aceFlags uint8) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		t.Fatalf("read Windows permissions for %q: %v", path, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if protected {
		owner, _, err := descriptor.Owner()
		if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
			t.Fatalf("owner = %v, %v; want process user %v", owner, err, user.User.Sid)
		}
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if got := control&windows.SE_DACL_PROTECTED != 0; got != protected {
		t.Fatalf("protected DACL = %v; want %v", got, protected)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil || dacl.AceCount == 0 {
		t.Fatalf("DACL must grant only process user access: %s", descriptor)
	}
	effectiveEntries := 0
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
			t.Fatalf("unexpected access principal on %q: %s", path, descriptor)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			if ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == 0 || (ace.Mask != 0x001f01ff && ace.Mask != windows.GENERIC_ALL) {
				t.Fatalf("unexpected inherited-only access on %q: %s", path, descriptor)
			}
			continue
		}
		effectiveEntries++
		if ace.Mask != 0x001f01ff || ace.Header.AceFlags != aceFlags {
			t.Fatalf("unexpected effective access on %q: %s", path, descriptor)
		}
	}
	if effectiveEntries != 1 {
		t.Fatalf("DACL has %d effective entries for %q; want one: %s", effectiveEntries, path, descriptor)
	}
}
