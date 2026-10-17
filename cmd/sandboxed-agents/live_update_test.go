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

// update performs one successful update and one forced rollback on a sandbox
// of the native toolchain set: the update moves it to the existing base image,
// and the forced rollback, with only the readiness probe's ssh faked to fail,
// restores that state. Host key and SSH authorization must survive both.
func (suite *liveSuite) update(t *testing.T) {
	host := suite.host
	const name = "live-update"
	const authorized = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	host.cleanupSandbox(t, name)
	host.mustCLI(t, "", "up", name, "--with", "native")
	native := host.inspectContainer(t, name)
	host.assertNoSwap(t, name, native)
	if native.Config.Labels[cli.ToolchainsLabel] != "native" {
		t.Fatalf("sandbox %s does not record the native toolchain set: %+v", name, native.Config.Labels)
	}
	args, err := cli.ManagerQueryArgs(cli.DerivedNames(host.group, name).Container, "/usr/local/bin/sandboxed-agents-manager", "ssh-authorize", authorized)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := host.podman(args...); err != nil {
		t.Fatalf("authorize test key: %v %s", err, output)
	}
	key := host.sshHostKey(t, name)
	output := host.mustCLI(t, "", "update", name, "--with", "none")
	if !strings.Contains(output, "updated") {
		t.Fatalf("update did not report the replacement:\n%s", output)
	}
	base := host.inspectContainer(t, name)
	host.assertNoSwap(t, name, base)
	if sameImageID(base.Image, native.Image) || base.Config.Labels[cli.ToolchainsLabel] != "none" {
		t.Fatalf("successful update did not select the base image: image=%s labels=%v", base.Image, base.Config.Labels)
	}
	host.assertListed(t, name, "running")
	host.assertSSHServer(t, name, key)
	failing := failingLiveUpdateHost(t, host, base.Config.Labels[cli.SSHPortLabel])
	output, err = failing.cli("", "update", name, "--with", "native", "--force")
	if err == nil || !strings.Contains(output, "readiness wait failed") || !strings.Contains(output, "restored sandbox") {
		t.Fatalf("forced rollback: %v %s", err, output)
	}
	restored := host.inspectContainer(t, name)
	host.assertNoSwap(t, name, restored)
	if !sameImageID(restored.Image, base.Image) || restored.Config.Labels[cli.ToolchainsLabel] != "none" {
		t.Fatalf("rollback did not restore the base image: image=%s labels=%v", restored.Image, restored.Config.Labels)
	}
	if output, err := host.podman("container", "exists", cli.DerivedNames(host.group, name).Backup); err == nil {
		t.Fatalf("rollback left the backup container behind: %s", output)
	}
	host.assertListed(t, name, "running")
	host.mustCLI(t, "", "check", name)
	if current := host.sshHostKey(t, name); current != key {
		t.Fatalf("update changed host key: before=%s after=%s", key, current)
	}
	if output := host.mustCLI(t, "cat /etc/ssh/authorized_keys\n", "shell", name); strings.TrimSpace(output) != authorized {
		t.Fatalf("authorization changed: %s", output)
	}
	host.assertSSHServer(t, name, key)
	host.mustCLI(t, "", "remove", name, "--volumes")
	host.assertNoSandboxObjects(t, name)
}

// failingLiveUpdateHost prepares the suite's forced readiness failure: ssh
// fails only for the readiness probe of the given port and otherwise runs the
// real program, so Podman's own machine transport keeps working.
func failingLiveUpdateHost(t *testing.T, host liveHost, port string) liveHost {
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
