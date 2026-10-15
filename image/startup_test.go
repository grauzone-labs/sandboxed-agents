package image_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestImageStartupInitializesSSHHostKeys proves the embedded startup command creates missing SSH host keys before replacing the startup shell with foreground sshd, preserves them on restart, and stops on generation failure through the real shell and OpenSSH process seams.
func TestImageStartupInitializesSSHHostKeys(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the image startup command runs on Linux")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skipf("host ssh-keygen is unavailable: %v", err)
	}
	recipe, err := fs.ReadFile(image.Context(), "base/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	entrypoint := imageEntrypoint(t, recipe)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// Redirect only the external commands: OpenSSH still generates real keys,
	// while sshd records which public key was ready when it began.
	for name, script := range map[string]string{
		"ssh-keygen": "#!/bin/sh\nexec \"$TEST_SSH_KEYGEN\" \"$@\" -f \"$TEST_SSH_ROOT\"\n",
		"sshd":       "#!/bin/sh\ncat \"$TEST_SSH_ROOT/etc/ssh/ssh_host_ed25519_key.pub\" > \"$TEST_SSH_ROOT/observed-key\"\nprintf '%s\\n' \"$$\" \"$@\" > \"$TEST_SSH_ROOT/sshd-start\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Substitute only the daemon process boundary; the startup shell and key
	// generator remain real, and exec must preserve the startup process ID.
	entrypoint[len(entrypoint)-1] = strings.ReplaceAll(entrypoint[len(entrypoint)-1], "/usr/sbin/sshd", filepath.Join(bin, "sshd"))
	run := func(generator string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, entrypoint[0], entrypoint[1:]...)
		command.WaitDelay = time.Second
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_SSH_ROOT="+root, "TEST_SSH_KEYGEN="+generator)
		output, err := command.CombinedOutput()
		t.Logf("startup: %s", output)
		if err == nil {
			started, readErr := os.ReadFile(filepath.Join(root, "sshd-start"))
			want := strconv.Itoa(command.Process.Pid) + "\n-D\n-e\n-f\n/usr/local/etc/sandboxed-agents/sshd_config\n"
			if readErr != nil || string(started) != want {
				t.Fatalf("startup did not exec foreground sshd with its trusted configuration: got %q, %v; want %q", started, readErr, want)
			}
		}
		return err
	}
	var original []byte
	for attempt := range 2 {
		if err := run(keygen); err != nil {
			t.Fatal(err)
		}
		key, err := os.ReadFile(filepath.Join(root, "observed-key"))
		if err != nil || !bytes.HasPrefix(key, []byte("ssh-ed25519 ")) {
			t.Fatalf("sshd began without a generated host key: %q, %v", key, err)
		}
		if attempt == 0 {
			original = key
		} else if !bytes.Equal(key, original) {
			t.Fatal("startup replaced the existing host key")
		}
	}
	if err := os.Remove(filepath.Join(root, "observed-key")); err != nil {
		t.Fatal(err)
	}
	if err := run("/bin/false"); err == nil {
		t.Fatal("startup succeeded despite host-key generation failure")
	}
	if _, err := os.Stat(filepath.Join(root, "observed-key")); !os.IsNotExist(err) {
		t.Fatalf("sshd ran after host-key generation failed: %v", err)
	}
}

// TestImageSSHPolicy proves the embedded policy permits only agent public-key authentication from the server state volume and disables agent forwarding through the real sshd configuration parser.
func TestImageSSHPolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the image SSH server runs on Linux")
	}
	sshd, err := exec.LookPath("/usr/sbin/sshd")
	if err != nil {
		t.Skipf("host sshd is unavailable: %v", err)
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skipf("host ssh-keygen is unavailable: %v", err)
	}
	configuration, err := fs.ReadFile(image.Context(), "base/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	file := filepath.Join(directory, "sshd_config")
	if err := os.WriteFile(file, configuration, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(directory, "host_key")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("generate temporary parser host key: %v\n%s", err, output)
	}
	// A temporary host key lets an unprivileged host parse the unchanged policy
	// without reading any private host key from the host's /etc/ssh.
	output, err := exec.CommandContext(ctx, sshd, "-T", "-f", file, "-h", key).Output()
	if err != nil {
		t.Fatalf("parse image SSH policy: %v", err)
	}
	settings := make(map[string][]string)
	for _, line := range strings.Split(string(output), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if ok {
			settings[name] = append(settings[name], value)
		}
	}
	for name, want := range map[string]string{
		"authorizedkeysfile": "/etc/ssh/authorized_keys", "authorizedkeyscommand": "none",
		"allowusers": "agent", "permitrootlogin": "no", "authenticationmethods": "publickey",
		"pubkeyauthentication": "yes", "passwordauthentication": "no", "kbdinteractiveauthentication": "no",
		"usepam": "yes", "allowagentforwarding": "no", "subsystem": "sftp internal-sftp",
	} {
		if got := strings.Join(settings[name], ","); got != want {
			t.Errorf("effective %s = %q; want %q", name, got, want)
		}
	}
}

// imageEntrypoint decodes the startup command shared by embedded and materialized recipe checks.
func imageEntrypoint(t *testing.T, recipe []byte) []string {
	t.Helper()
	for _, line := range strings.Split(string(recipe), "\n") {
		if value, ok := strings.CutPrefix(line, "ENTRYPOINT "); ok {
			var entrypoint []string
			if err := json.Unmarshal([]byte(value), &entrypoint); err != nil {
				t.Fatal(err)
			}
			if len(entrypoint) == 0 {
				t.Fatal("image recipe has an empty startup command")
			}
			return entrypoint
		}
	}
	t.Fatal("image recipe has no startup command")
	return nil
}
