package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
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
			testutil.AssertWindowsSSHPathAccess(t, path, true, 0)
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
	testutil.AssertWindowsSSHPathAccess(t, directory, true, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
	config := filepath.Join(directory, "config")
	if err := os.WriteFile(config, []byte("Host agent01\n"), 0600); err != nil {
		t.Fatalf("process user cannot create a file in secured directory: %v", err)
	}
	testutil.AssertWindowsSSHPathAccess(t, config, false, windows.INHERITED_ACE)
	nested := filepath.Join(directory, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatalf("process user cannot create a child directory: %v", err)
	}
	testutil.AssertWindowsSSHPathAccess(t, nested, false, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE|windows.INHERITED_ACE)
	key := filepath.Join(nested, "id_ed25519")
	if err := os.WriteFile(key, []byte("key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testutil.AssertWindowsSSHPathAccess(t, key, false, windows.INHERITED_ACE)
}

// TestSecureSSHReplacementWindowsPreservesConfigPermissions proves through
// secureSSHReplacement that an atomic replacement retains a user's custom DACL
// and parent inheritance protection on Windows.
func TestSecureSSHReplacementWindowsPreservesConfigPermissions(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(fmt.Sprintf("protected-%v", protected), func(t *testing.T) {
			directory := t.TempDir()
			previous := filepath.Join(directory, "config")
			replacement := filepath.Join(directory, ".replacement")
			for _, path := range []string{previous, replacement} {
				if err := os.WriteFile(path, []byte("user bytes\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:(A;;FA;;;%s)(A;;FR;;;BA)", user.User.Sid, user.User.Sid))
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			information := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			if protected {
				information = windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetNamedSecurityInfo(previous, windows.SE_FILE_OBJECT, information, user.User.Sid, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			before := testutil.SnapshotWindowsSSHPermissions(t, previous)
			if err := secureSSHReplacement(replacement, 0600, previous); err != nil {
				t.Fatal(err)
			}
			after := testutil.SnapshotWindowsSSHPermissions(t, replacement)
			if before.Descriptor != after.Descriptor || before.Control != after.Control {
				t.Fatalf("replacement permissions = %+v; want %+v", after, before)
			}
			if data, err := os.ReadFile(previous); err != nil || string(data) != "user bytes\n" {
				t.Fatalf("source changed = %q / %v", data, err)
			}
		})
	}
}

// TestSecureSSHReplacementWindowsAcceptsUnassignableOwner proves through
// secureSSHReplacement that replacing a config owned by Administrators does
// not require the process to assign that owner on Windows.
func TestSecureSSHReplacementWindowsAcceptsUnassignableOwner(t *testing.T) {
	directory := t.TempDir()
	previous := filepath.Join(directory, "config")
	replacement := filepath.Join(directory, ".replacement")
	for _, path := range []string{previous, replacement} {
		if err := os.WriteFile(path, []byte("user bytes\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:BAD:P(A;;FA;;;%s)(A;;FR;;;BA)", user.User.Sid))
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(previous, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil); err != nil {
		t.Skipf("process cannot assign the Administrators owner needed for this fixture: %v", err)
	}
	before := testutil.SnapshotWindowsSSHPermissions(t, previous)
	if err := secureSSHReplacement(replacement, 0600, previous); err != nil {
		t.Fatalf("replace config with Administrators owner: %v", err)
	}
	testutil.AssertWindowsSSHAccessControlUnchanged(t, replacement, before)
	replacementDescriptor, err := windows.GetNamedSecurityInfo(replacement, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	newOwner, _, err := replacementDescriptor.Owner()
	if err != nil || newOwner == nil || !newOwner.Equals(user.User.Sid) {
		t.Fatalf("replacement owner = %v, %v; want process user %v", newOwner, err, user.User.Sid)
	}
}

// TestSecureSSHPathWindowsOpenSSHAcceptsSetup proves through secureSSHPath and
// native Windows OpenSSH that secured private keys and included user and managed
// configurations are accepted after the same files with broad access are refused.
func TestSecureSSHPathWindowsOpenSSHAcceptsSetup(t *testing.T) {
	ssh, keygen := testutil.NativeWindowsOpenSSH(t)
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
	if output, err := testutil.RunWindowsSSH(t, keygen, "-q", "-t", "ed25519", "-N", "", "-f", key); err != nil {
		t.Fatalf("generate temporary key: %v\n%s", err, output)
	}
	grantEveryoneSSHPath(t, key)
	output, err := testutil.RunWindowsSSH(t, keygen, "-y", "-P", "", "-f", key)
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
	output, err = testutil.RunWindowsSSH(t, keygen, "-y", "-P", "", "-f", key)
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
		output, err = testutil.RunWindowsSSH(t, ssh, "-G", "-F", wrapper, "permission-adapter")
		if err == nil || !strings.Contains(strings.ToLower(string(output)), "permission") {
			t.Fatalf("OpenSSH did not reject the broad configuration permissions before securing %q: %v\n%s", path, err, output)
		}
		if err := secureSSHPath(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err = testutil.RunWindowsSSH(t, ssh, "-G", "-F", wrapper, "permission-adapter")
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
