package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"golang.org/x/sys/windows"
)

// TestSSHSetupWindowsInstallSecuresNewPathsAndPreservesExistingPermissions proves
// through the built CLI that opt-in installation creates Windows SSH
// files accepted by native OpenSSH and that install/remove preserve an existing
// configuration's bytes and ACL settings while assigning the CLI account as owner.
func TestSSHSetupWindowsInstallSecuresNewPathsAndPreservesExistingPermissions(t *testing.T) {
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("nested offline guard isolates PATH to fake programs; the outer suite checks native Windows OpenSSH")
	}
	ssh, keygen := testutil.NativeWindowsOpenSSH(t)
	t.Logf("process token elevated: %v", windows.GetCurrentProcessToken().IsElevated())
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-config-%v", existing), func(t *testing.T) {
			responses := installResponses()
			delete(responses, "exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-authorize "+setupPublicKey)
			responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-authorize *"] = lifecycleResponse{}
			fakes := sshSetupHarness(t, responses)
			// The fake records the CLI's invocation and delegates this one
			// program to its original native path without synthetic key writes.
			t.Setenv("TEST_SSH_KEYGEN_REAL_EXECUTABLE", keygen)
			userDirectory := filepath.Join(os.Getenv("USERPROFILE"), ".ssh")
			userConfig := filepath.Join(userDirectory, "config")
			original := []byte("# personal Windows settings\r\nHost personal\r\n    HostName example.com\r\n")
			var beforeConfig, beforeDirectory testutil.WindowsSSHPermissions
			if existing {
				if err := os.Mkdir(userDirectory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(userConfig, original, 0600); err != nil {
					t.Fatal(err)
				}
				setupCustomWindowsSSHPermissions(t, userDirectory)
				setupCustomWindowsSSHPermissions(t, userConfig)
				beforeConfig = testutil.SnapshotWindowsSSHPermissions(t, userConfig)
				beforeDirectory = testutil.SnapshotWindowsSSHPermissions(t, userDirectory)
			}
			if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
				t.Fatalf("install=%d %q %q", status, stdout, stderr)
			}
			status, entry, stderr := sshSetupRun(t, "ssh-config", "demo")
			if status != 0 || stderr != "" {
				t.Fatalf("installed entry=%d %q %q", status, entry, stderr)
			}
			identity := windowsSSHEntryPath(t, entry, "IdentityFile")
			knownHosts := windowsSSHEntryPath(t, entry, "UserKnownHostsFile")
			managedDirectory := filepath.Dir(filepath.Dir(identity))
			managedConfig := filepath.Join(managedDirectory, "config")
			for _, path := range []string{identity, identity + ".pub", knownHosts, filepath.Join(filepath.Dir(identity), "entry"), managedConfig, managedDirectory, filepath.Dir(identity)} {
				flags := uint8(0)
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
				}
				testutil.AssertWindowsSSHPathAccess(t, path, true, flags)
			}
			if existing {
				afterConfig := testutil.SnapshotWindowsSSHPermissions(t, userConfig)
				_, beforeDACL, _ := strings.Cut(beforeConfig.Descriptor, "D:")
				_, afterDACL, _ := strings.Cut(afterConfig.Descriptor, "D:")
				if beforeDACL != afterDACL || beforeConfig.Control != afterConfig.Control {
					t.Fatalf("user config ACL changed: before=%+v after=%+v", beforeConfig, afterConfig)
				}
				testutil.AssertWindowsSSHOwnerIsProcessUser(t, userConfig)
				testutil.AssertWindowsSSHPermissionsUnchanged(t, userDirectory, beforeDirectory)
			} else {
				testutil.AssertWindowsSSHPathAccess(t, userConfig, true, 0)
				testutil.AssertWindowsSSHPathAccess(t, userDirectory, true, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
				original = nil
				beforeConfig = testutil.SnapshotWindowsSSHPermissions(t, userConfig)
				beforeDirectory = testutil.SnapshotWindowsSSHPermissions(t, userDirectory)
			}
			installed, err := os.ReadFile(userConfig)
			if err != nil || strings.Count(string(installed), "Include ") != 1 || !strings.HasSuffix(string(installed), string(original)) {
				t.Fatalf("installed user configuration=%q error=%v; original=%q", installed, err, original)
			}
			pin, err := os.ReadFile(knownHosts)
			if err != nil || string(pin) != "[127.0.0.1]:2222 "+setupPublicKey+"\n" {
				t.Fatalf("pinned host key=%q error=%v", pin, err)
			}
			public, err := os.ReadFile(identity + ".pub")
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Fields(string(public))
			if len(fields) < 2 || fields[0] != "ssh-ed25519" {
				t.Fatalf("generated public key=%q", public)
			}
			publicKey := strings.Join(fields[:2], " ")
			if publicKey == setupPublicKey {
				t.Fatal("key generation used the synthetic fake key instead of native OpenSSH")
			}
			output, err := testutil.RunWindowsSSH(t, keygen, "-y", "-P", "", "-f", identity)
			if err != nil || strings.TrimSpace(string(output)) != publicKey {
				t.Fatalf("native OpenSSH refused generated private key: %v\n%s", err, output)
			}
			keygenCalls := testutil.Invocations(t, filepath.Join(fakes.Dir, "keygen.jsonl"))
			if len(keygenCalls) != 1 {
				t.Fatalf("CLI generated %d dedicated keys; want one: %v", len(keygenCalls), keygenCalls)
			}
			assertWindowsSSHManagerCall(t, fakes.PodmanLog, "ssh-authorize", publicKey)
			// Explicit -F bypasses its file's ACL check. Include a temporary
			// user config from a wrapper so both config ACLs are checked.
			wrapper := filepath.Join(t.TempDir(), "wrapper-config")
			if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("Include %q\n", filepath.ToSlash(userConfig))), 0600); err != nil {
				t.Fatal(err)
			}
			output, err = testutil.RunWindowsSSH(t, ssh, "-G", "-F", wrapper, "demo")
			if err != nil {
				t.Fatalf("native OpenSSH refused installed configuration: %v\n%s", err, output)
			}
			resolved := strings.ReplaceAll(string(output), "\r\n", "\n")
			for _, option := range []string{"hostname 127.0.0.1\n", "user agent\n", "port 2222\n", "identitiesonly yes\n", "stricthostkeychecking true\n"} {
				if !strings.Contains(resolved, option) {
					t.Fatalf("native OpenSSH did not resolve %q from installed config:\n%s", option, resolved)
				}
			}
			if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove"); status != 0 {
				t.Fatalf("remove=%d %q %q", status, stdout, stderr)
			}
			assertWindowsSSHManagerCall(t, fakes.PodmanLog, "ssh-deauthorize")
			after, err := os.ReadFile(userConfig)
			if err != nil || string(after) != string(original) {
				t.Fatalf("removed user configuration=%q error=%v; want original %q", after, err, original)
			}
			testutil.AssertWindowsSSHPermissionsUnchanged(t, userConfig, beforeConfig)
			testutil.AssertWindowsSSHPermissionsUnchanged(t, userDirectory, beforeDirectory)
			for _, path := range []string{identity, identity + ".pub", knownHosts, managedConfig} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("removed managed path %q still exists or cannot be checked: %v", path, err)
				}
			}
		})
	}
}

func setupCustomWindowsSSHPermissions(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// An independent, OpenSSH-compatible custom DACL differs from the
	// managed sole-user DACL, so replacing it with managed access fails.
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FR;;;BA)", user.User.Sid))
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
		t.Fatalf("set custom SSH DACL on %q: %v", path, err)
	}
}

func assertWindowsSSHManagerCall(t *testing.T, log, command string, arguments ...string) {
	t.Helper()
	want := strings.Join(append([]string{"exec", "--user=0:0", "sandboxed-agents.default.demo", "/usr/local/bin/sandboxed-agents-manager", command}, arguments...), " ")
	for _, call := range testutil.Invocations(t, log) {
		if strings.Join(call, " ") == "--connection chosen "+want {
			return
		}
	}
	t.Fatalf("missing root manager call: %s", want)
}

func windowsSSHEntryPath(t *testing.T, entry, directive string) string {
	t.Helper()
	for _, line := range strings.Split(entry, "\n") {
		if raw, ok := strings.CutPrefix(strings.TrimSpace(line), directive+" "); ok {
			path, err := strconv.Unquote(raw)
			if err != nil {
				t.Fatal(err)
			}
			return strings.ReplaceAll(path, "%%", "%")
		}
	}
	t.Fatalf("installed entry has no %s path: %s", directive, entry)
	return ""
}
