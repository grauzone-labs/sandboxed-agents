package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestLiveUpdateFailurePreservesMachineSSH proves the live suite's readiness injection preserves nested Podman machine SSH through liveHost.cli and executable process fixtures.
func TestLiveUpdateFailurePreservesMachineSSH(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transport path with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	driver := filepath.Join(dir, "driver"+suffix)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(fakeExecutable), "live-update-transport"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"driver", "podman", "ssh"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, platform := range []string{"windows", "linux"} {
		t.Run(platform, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "real-ssh.jsonl")
			host := liveHost{binary: driver, environment: testutil.Env(os.Environ(), map[string]string{"TEST_TRANSPORT_LOG": log})}
			failing := failingLiveUpdateHost(t, host, "2222")
			out, err := failing.cli("", platform)
			if err != nil || !strings.Contains(out, "readiness-failed-after-preflight") {
				t.Fatalf("transport/readiness sequence: %v %s", err, out)
			}
			calls := testutil.Invocations(t, log)
			want := 0
			if platform == "windows" {
				want = 3
			}
			if len(calls) != want {
				t.Fatalf("readiness leaked to original SSH or transport missing: %v", calls)
			}
			if platform == "windows" {
				for i, query := range [][]string{{"id", "-u"}, {"cat", "/sys/fs/cgroup/cgroup.controllers"}, {"test", "-e", "/etc/wsl.conf"}} {
					expected := append([]string{"-i", "machine key with spaces", "-p", "54321", "core@127.0.0.1"}, query...)
					if !slices.Equal(calls[i], expected) {
						t.Fatalf("machine argv changed: %v", calls[i])
					}
				}
			}
			// Similar calls must pass through: a different port, user, or known_hosts policy.
			for _, args := range [][]string{
				{"-o", "UserKnownHostsFile=known_hosts", "-p", "2223", "agent@127.0.0.1"},
				{"-o", "UserKnownHostsFile=known_hosts", "-p", "2222", "core@127.0.0.1"},
				{"-o", "UserKnownHostsFile=machine hosts", "-p", "2222", "agent@127.0.0.1"},
			} {
				out, err := failing.cli("", append([]string{"forward"}, args...)...)
				if err != nil || out != "transport-ok" {
					t.Fatalf("non-readiness call intercepted: %v %s", err, out)
				}
				calls = testutil.Invocations(t, log)
				if !slices.Equal(calls[len(calls)-1], args) {
					t.Fatalf("forwarded argv changed: %v", calls)
				}
			}
			out, err = failing.cli("stdin with spaces\n", "forward", "--fixture-streams", "literal $() & arg")
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 || !strings.Contains(out, "stdin with spaces\n") || !strings.Contains(out, "transport-stderr") {
				t.Fatalf("forwarded streams/status changed: %v %q", err, out)
			}
		})
	}
}
