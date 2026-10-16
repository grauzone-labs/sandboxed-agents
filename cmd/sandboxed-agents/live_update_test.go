package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// verifyLiveUpdates extends the separately opted-in image suite on both hosts:
// real replacement and real Podman rollback, with only the failing SSH probe faked.
func verifyLiveUpdates(t *testing.T, host liveImageHost, old, newImages map[string]liveImage) {
	t.Helper()
	const authorized = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, set := range []string{"none", "native"} {
		name := "image-" + set
		args, err := cli.ManagerQueryArgs(cli.DerivedNames(host.group, name).Container, "/usr/local/bin/sandboxed-agents-manager", "ssh-authorize", authorized)
		if err != nil {
			t.Fatal(err)
		}
		if output, err := host.podman(args...); err != nil {
			t.Fatalf("authorize test key: %v %s", err, output)
		}
		key := host.sshHostKey(t, name)
		host.mustCLI(t, "", "stop", name)
		host.mustCLI(t, "", "start", name)
		if set == "none" {
			host.mustCLI(t, "", "update", name)
			if !sameImageID(host.inspectContainer(t, name).Image, newImages[set].ID) {
				t.Fatal("successful update did not select new image")
			}
		} else {
			failing := failingLiveUpdateHost(t, host, host.inspectContainer(t, name).Config.Labels[cli.SSHPortLabel])
			output, err := failing.cli("", "update", name, "--force")
			if err == nil || !strings.Contains(output, "readiness wait failed") || !strings.Contains(output, "restored sandbox") {
				t.Fatalf("forced rollback: %v %s", err, output)
			}
			if !sameImageID(host.inspectContainer(t, name).Image, old[set].ID) {
				t.Fatal("rollback did not restore old image")
			}
		}
		if current := host.sshHostKey(t, name); current != key {
			t.Fatalf("update changed host key: before=%s after=%s", key, current)
		}
		if output := host.mustCLI(t, "cat /etc/ssh/authorized_keys\n", "shell", name); strings.TrimSpace(output) != authorized {
			t.Fatalf("authorization changed: %s", output)
		}
	}
}

// failingLiveUpdateHost prepares the image suite's forced readiness failure.
func failingLiveUpdateHost(t *testing.T, host liveImageHost, port string) liveImageHost {
	t.Helper()
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	realSSH, err = filepath.Abs(realSSH)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binary, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ssh"+suffix), binary, 0700); err != nil {
		t.Fatal(err)
	}
	failing := host
	failing.environment = testutil.Env(host.environment, map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "TEST_SSH_LOG": filepath.Join(dir, "ssh.jsonl"), "TEST_SSH_OUTPUT": "", "TEST_SSH_RESPONSES": "", "TEST_SSH_KNOWN_HOSTS": "", "TEST_SSH_EXIT": "255", "TEST_SSH_REAL_EXECUTABLE": realSSH, "TEST_SSH_FAIL_PORT": port})
	return failing
}
