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
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestImageStartupInitializesSSHHostKeys proves the embedded startup command creates missing SSH host keys before keepalive, preserves them on restart, and stops on generation failure through the real shell and OpenSSH process seams.
func TestImageStartupInitializesSSHHostKeys(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the image startup command runs on Linux")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatal(err)
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
	// while keepalive records which public key was ready when it began.
	for name, script := range map[string]string{
		"ssh-keygen": "#!/bin/sh\nexec \"$TEST_SSH_KEYGEN\" \"$@\" -f \"$TEST_SSH_ROOT\"\n",
		"sleep":      "#!/bin/sh\ncat \"$TEST_SSH_ROOT/etc/ssh/ssh_host_ed25519_key.pub\" > \"$TEST_SSH_ROOT/observed-key\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	run := func(generator string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, entrypoint[0], entrypoint[1:]...)
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_SSH_ROOT="+root, "TEST_SSH_KEYGEN="+generator)
		output, err := command.CombinedOutput()
		t.Logf("startup: %s", output)
		return err
	}
	var original []byte
	for attempt := range 2 {
		if err := run(keygen); err != nil {
			t.Fatal(err)
		}
		key, err := os.ReadFile(filepath.Join(root, "observed-key"))
		if err != nil || !bytes.HasPrefix(key, []byte("ssh-ed25519 ")) {
			t.Fatalf("keepalive began without a generated host key: %q, %v", key, err)
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
		t.Fatalf("keepalive ran after host-key generation failed: %v", err)
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
