package liverun_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestRunEndsInvocationLeftRunningBySuite proves the Linux supervisor ends an
// owned invocation group that the suite started and left running when it
// exited, before it publishes the suite's completion. It exercises the run
// command, the owned-group records and the process-group seam.
func TestRunEndsInvocationLeftRunningBySuite(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	t.Setenv("LIVERUN_FIXTURE_ORPHAN_INVOCATION", "1")
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	record, err := os.ReadFile(filepath.Join(root, "invocation"))
	if err != nil {
		t.Fatal(err)
	}
	name, _, _ := strings.Cut(string(record), ".")
	pid, err := strconv.Atoi(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if invocationRunning(pid, string(record)) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	if invocationRunning(pid, string(record)) {
		t.Fatalf("invocation group %d still runs after the suite's completion was published", pid)
	}
}

// invocationRunning reports whether the process recorded as PID.STARTTICK still
// runs: it exists with that start tick and is neither dead nor a zombie.
func invocationRunning(pid int, record string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data[strings.LastIndex(string(data), ")")+1:]))
	return len(fields) >= 20 && fields[0] != "Z" && fields[0] != "X" && fmt.Sprintf("%d.%s", pid, fields[19]) == record
}
