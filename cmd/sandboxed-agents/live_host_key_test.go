package main_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// The fake podman exec reads its manager storage root and its simulated
// start-up overhead from these variables.
const (
	fakeManagerExecRootEnv     = "TEST_FAKE_MANAGER_EXEC_ROOT"
	fakeManagerExecOverheadEnv = "TEST_FAKE_MANAGER_EXEC_OVERHEAD"
)

// fakeManagerExec stands in for `podman exec --user=0:0 CONTAINER MANAGER ARGS...`: it sleeps
// for the configured start-up overhead, then runs the real manager command as container root with
// its file reads rooted at root, and returns the manager's exit status.
func fakeManagerExec(root string, args []string) int {
	if len(args) < 5 || args[0] != "exec" || args[1] != "--user=0:0" || args[3] != "/usr/local/bin/sandboxed-agents-manager" {
		fmt.Fprintf(os.Stderr, "fake podman: unexpected arguments %q\n", args)
		return 125
	}
	if overhead, err := time.ParseDuration(os.Getenv(fakeManagerExecOverheadEnv)); err == nil {
		time.Sleep(overhead)
	}
	effects := manager.Effects{
		Identity: func() manager.Identity { return manager.Identity{UID: 0, GID: 0} },
		ReadFile: func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(path))) },
	}
	return manager.RunWithEffects(args[4:], os.Stdout, os.Stderr, effects)
}

// fakeManagerHost installs a copy of the test binary as the only podman on PATH, so the live
// helper's real podman command runs fakeManagerExec with the given start-up overhead, and returns
// a liveHost bound to it and the root under which the fake manager reads its files.
func fakeManagerHost(t *testing.T, overhead time.Duration) (liveHost, string) {
	t.Helper()
	bin, root := t.TempDir(), t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	podman := filepath.Join(bin, "podman"+suffix)
	if err := os.Link(testExecutable, podman); err != nil {
		copyFile(t, testExecutable, podman)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc", "ssh"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	environment := append(os.Environ(), fakeManagerExecRootEnv+"="+root, fakeManagerExecOverheadEnv+"="+overhead.String())
	return liveHost{group: "test", environment: environment}, root
}

// copyFile copies the executable at source to target with execute permission.
func copyFile(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// publishHostKeyAfter writes the public host key under root after delay, through a rename so the
// fake manager never reads a partial file, as the entrypoint's ssh-keygen publishes it at start-up.
func publishHostKeyAfter(t *testing.T, root string, delay time.Duration) {
	t.Helper()
	path := filepath.Join(root, "etc", "ssh", "ssh_host_ed25519_key.pub")
	timer := time.AfterFunc(delay, func() {
		if err := os.WriteFile(path+".tmp", []byte(liveTestHostKey+" root@sandbox\n"), 0644); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { timer.Stop() })
}

// liveTestHostKey is a syntactically valid Ed25519 public key for the fake manager to publish.
const liveTestHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// TestLiveSSHHostKeyWaitsForManagerAfterExecStartup proves the live helper returns the key when
// podman exec start-up plus the manager's two-second wait for the key ends after three seconds,
// the deadline at which the helper used to kill the query, through the helper's real podman
// command and a fake podman exec that runs the real manager.
func TestLiveSSHHostKeyWaitsForManagerAfterExecStartup(t *testing.T) {
	host, root := fakeManagerHost(t, 1500*time.Millisecond)
	publishHostKeyAfter(t, root, 3200*time.Millisecond)
	key, err := host.readSSHHostKey("demo")
	if err != nil || key != liveTestHostKey {
		t.Fatalf("key=%q error=%v", key, err)
	}
}

// TestLiveSSHHostKeyReportsManagerFailure proves that when the key is still missing at the end of
// the manager's wait, the live helper reports the manager's exit status, its stderr naming the
// key file, and the elapsed time instead of a deadline kill, through the helper's real podman
// command and a fake podman exec that runs the real manager.
func TestLiveSSHHostKeyReportsManagerFailure(t *testing.T) {
	host, _ := fakeManagerHost(t, 1500*time.Millisecond)
	key, err := host.readSSHHostKey("demo")
	if err == nil || key != "" {
		t.Fatalf("missing key was accepted: key=%q", key)
	}
	message := err.Error()
	for _, fragment := range []string{"read live SSH host key for demo after ", "exit status 1", "manager stderr: ", "ssh_host_ed25519_key.pub"} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("failure omits %q:\n%s", fragment, message)
		}
	}
	if strings.Contains(message, "deadline exceeded") || strings.Contains(message, "killed") {
		t.Fatalf("manager failure was reported as a deadline kill:\n%s", message)
	}
}
