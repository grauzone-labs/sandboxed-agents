package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestSecureSSHPathWindowsRestrictsManagedFiles proves through secureSSHPath
// that Windows files get a protected DACL granting only the process user access.
func TestSecureSSHPathWindowsRestrictsManagedFiles(t *testing.T) {
	for _, name := range []string{"id_ed25519", "id_ed25519.pub", "known_hosts", "config"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			const contents = "managed SSH setup\n"
			if err := os.WriteFile(path, []byte(contents), 0666); err != nil {
				t.Fatal(err)
			}
			grantEveryoneSSHPath(t, path)
			mode := os.FileMode(0600)
			if name == "id_ed25519.pub" {
				mode = 0644
			}
			if err := secureSSHPath(path, mode); err != nil {
				t.Fatal(err)
			}
			assertSSHPathAccess(t, path, true, 0)
			got, err := os.ReadFile(path)
			if err != nil || string(got) != contents {
				t.Fatalf("secured file contents = %q, %v; want %q", got, err, contents)
			}
			if err := os.WriteFile(path, []byte("updated\n"), 0600); err != nil {
				t.Fatalf("process user cannot update secured file: %v", err)
			}
		})
	}
}

// TestSecureSSHPathWindowsDirectoriesRestrictChildren proves through
// secureSSHPath that a new Windows SSH directory grants only the process user
// inherited access to subsequently created files and directories.
func TestSecureSSHPathWindowsDirectoriesRestrictChildren(t *testing.T) {
	directory := filepath.Join(t.TempDir(), ".ssh")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	grantEveryoneSSHPath(t, directory)
	if err := secureSSHPath(directory, os.ModeDir|0700); err != nil {
		t.Fatal(err)
	}
	assertSSHPathAccess(t, directory, true, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
	config := filepath.Join(directory, "config")
	if err := os.WriteFile(config, []byte("Host agent01\n"), 0600); err != nil {
		t.Fatalf("process user cannot create a file in secured directory: %v", err)
	}
	assertSSHPathAccess(t, config, false, windows.INHERITED_ACE)
	nested := filepath.Join(directory, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatalf("process user cannot create a child directory: %v", err)
	}
	assertSSHPathAccess(t, nested, false, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE|windows.INHERITED_ACE)
	key := filepath.Join(nested, "id_ed25519")
	if err := os.WriteFile(key, []byte("key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertSSHPathAccess(t, key, false, windows.INHERITED_ACE)
}

// TestSecureSSHPathWindowsOpenSSHAcceptsSetup proves through secureSSHPath and
// native Windows OpenSSH that secured private keys and included user and managed
// configurations are accepted after the same files with broad access are refused.
func TestSecureSSHPathWindowsOpenSSHAcceptsSetup(t *testing.T) {
	ssh, keygen := nativeWindowsOpenSSH(t)
	t.Logf("process token elevated: %v", windows.GetCurrentProcessToken().IsElevated())
	root := t.TempDir()
	userDirectory := filepath.Join(root, ".ssh")
	managedDirectory := filepath.Join(root, "state")
	for _, directory := range []string{userDirectory, managedDirectory} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := secureSSHPath(directory, os.ModeDir|0700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(managedDirectory, "id_ed25519")
	if output, err := runWindowsSSH(t, keygen, "-q", "-t", "ed25519", "-N", "", "-f", key); err != nil {
		t.Fatalf("generate temporary key: %v\n%s", err, output)
	}
	grantEveryoneSSHPath(t, key)
	output, err := runWindowsSSH(t, keygen, "-y", "-P", "", "-f", key)
	if err == nil || !strings.Contains(strings.ToLower(string(output)), "permission") {
		t.Fatalf("OpenSSH did not reject the broad private-key permissions: %v\n%s", err, output)
	}
	for _, path := range []string{key, key + ".pub"} {
		if err := secureSSHPath(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(publicKey))
	if len(fields) < 2 {
		t.Fatalf("generated public key is invalid: %q", publicKey)
	}
	output, err = runWindowsSSH(t, keygen, "-y", "-P", "", "-f", key)
	if err != nil || strings.TrimSpace(string(output)) != strings.Join(fields[:2], " ") {
		t.Fatalf("OpenSSH did not load the secured private key: %v\n%s", err, output)
	}
	managedConfig := filepath.Join(managedDirectory, "config")
	userConfig := filepath.Join(userDirectory, "config")
	// OpenSSH's explicit -F file bypasses its ownership check. Loading both
	// configuration files through Include exercises their permission checks.
	wrapper := filepath.Join(root, "wrapper-config")
	contents := map[string]string{
		managedConfig: "Host permission-adapter\n  HostName 127.0.0.1\n  User agent\n  Port 2222\n",
		userConfig:    fmt.Sprintf("Include %q\n", filepath.ToSlash(managedConfig)),
		wrapper:       fmt.Sprintf("Include %q\n", filepath.ToSlash(userConfig)),
	}
	for path, body := range contents {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	grantEveryoneSSHPath(t, userConfig)
	grantEveryoneSSHPath(t, managedConfig)
	for _, path := range []string{userConfig, managedConfig} {
		output, err = runWindowsSSH(t, ssh, "-G", "-F", wrapper, "permission-adapter")
		if err == nil || !strings.Contains(strings.ToLower(string(output)), "permission") {
			t.Fatalf("OpenSSH did not reject the broad configuration permissions before securing %q: %v\n%s", path, err, output)
		}
		if err := secureSSHPath(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err = runWindowsSSH(t, ssh, "-G", "-F", wrapper, "permission-adapter")
	if err != nil {
		t.Fatalf("OpenSSH refused secured configuration: %v\n%s", err, output)
	}
	resolved := strings.ReplaceAll(string(output), "\r\n", "\n")
	for _, option := range []string{"hostname 127.0.0.1\n", "user agent\n", "port 2222\n"} {
		if !strings.Contains(resolved, option) {
			t.Fatalf("OpenSSH did not resolve %q from secured configuration:\n%s", option, resolved)
		}
	}
}

func nativeWindowsOpenSSH(t *testing.T) (ssh, keygen string) {
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
		output, err := runWindowsSSH(t, ssh, "-V")
		if err == nil && strings.Contains(string(output), "OpenSSH_for_Windows") {
			return ssh, keygen
		}
	}
	t.Skip("native Windows OpenSSH executables are unavailable")
	return "", ""
}

func runWindowsSSH(t *testing.T, executable string, arguments ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, arguments...).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("OpenSSH command timed out: %v", ctx.Err())
	}
	return output, err
}

func grantEveryoneSSHPath(t *testing.T, path string) {
	t.Helper()
	// The independent SDDL fixture deliberately allows Everyone full access.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func assertSSHPathAccess(t *testing.T, path string, protected bool, aceFlags uint8) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor == nil {
		t.Fatal("secured path has no security descriptor")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// Directly secured paths must belong to the process user. Child creation
	// selects a default owner from the token; inheritance promises only access.
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
	if dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("DACL must grant only the process user access: %s", descriptor)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(user.User.Sid) {
		t.Fatalf("sole access entry = %s; want allow for process user %v", descriptor, user.User.Sid)
	}
	// FILE_ALL_ACCESS is defined by Win32 as 0x001f01ff, independently of
	// how the adapter builds or maps the access entry.
	if ace.Mask != 0x001f01ff || ace.Header.AceFlags != aceFlags {
		t.Fatalf("access mask = %#x, flags = %#x; want full file access and flags %#x", ace.Mask, ace.Header.AceFlags, aceFlags)
	}
}
