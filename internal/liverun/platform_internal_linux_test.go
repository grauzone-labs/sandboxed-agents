package liverun

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestAvailableBytesUsesFragmentSize proves the Linux free-space reader counts
// available blocks in fragment-size units, falling back to the block size when
// the filesystem reports no fragment size. It exercises the statfs result seam.
func TestAvailableBytesUsesFragmentSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		stat unix.Statfs_t
		want uint64
	}{
		{"fragment", unix.Statfs_t{Bavail: 100000, Bsize: 4096, Frsize: 512}, 51200000},
		{"no fragment", unix.Statfs_t{Bavail: 100000, Bsize: 4096}, 409600000},
	} {
		if got := availableBytes(tc.stat); got != tc.want {
			t.Errorf("%s: availableBytes = %d; want %d", tc.name, got, tc.want)
		}
	}
}

// fakeProcess is one /proc/PID/stat entry in a fixture process table.
type fakeProcess struct {
	pid, pgrp int
	state     string
	start     uint64
	session   int
}

// writeProcTable writes a fixture /proc tree whose stat lines carry state, process group, session and start tick.
func writeProcTable(t *testing.T, processes ...fakeProcess) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range processes {
		directory := filepath.Join(root, fmt.Sprint(p.pid))
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		// Fields after the command: state ppid pgrp session ... with the start tick as the 20th.
		fields := []string{p.state, "1", fmt.Sprint(p.pgrp), fmt.Sprint(p.session)}
		for len(fields) < 19 {
			fields = append(fields, "0")
		}
		fields = append(fields, fmt.Sprint(p.start), "0", "0")
		line := fmt.Sprintf("%d (go test) %s\n", p.pid, strings.Join(fields, " "))
		if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGroupAliveBindsGroupToRecordedLeader proves the Linux suite liveness check
// still finds descendants of the original group after its leader exits, but a
// process group whose ID was reused after the original group ended does not
// count as the suite. It exercises the /proc process-group liveness seam.
func TestGroupAliveBindsGroupToRecordedLeader(t *testing.T) {
	ref := processRef{PID: 100, Identity: "500"}
	for _, tc := range []struct {
		name      string
		processes []fakeProcess
		want      bool
	}{
		{"live leader", []fakeProcess{{100, 100, "S", 500, 0}}, true},
		{"descendant after leader exit", []fakeProcess{{230, 100, "S", 900, 0}}, true},
		{"descendant of zombie leader", []fakeProcess{{100, 100, "Z", 500, 0}, {230, 100, "S", 900, 0}}, true},
		{"reused leader ID", []fakeProcess{{100, 100, "S", 7000, 0}, {231, 100, "S", 7100, 0}}, false},
		{"member predating the leader", []fakeProcess{{50, 100, "S", 400, 0}}, false},
		{"group ended", []fakeProcess{{230, 230, "S", 900, 0}}, false},
	} {
		if got := groupAlive(writeProcTable(t, tc.processes...), ref); got != tc.want {
			t.Errorf("%s: groupAlive = %t; want %t", tc.name, got, tc.want)
		}
	}
}

// TestOwnedGroupRunningKillsOnlyVerifiedGroups proves the supervisor treats a
// recorded invocation group as running only when its leader still has the
// recorded start tick, or a live member of the original group remains, and the
// group belongs to the supervisor's session; an unreadable process table or a
// record without a start tick verifies nothing. It exercises the /proc
// process-group seam behind abortOwnedGroups.
func TestOwnedGroupRunningKillsOnlyVerifiedGroups(t *testing.T) {
	ref := processRef{PID: 100, Identity: "500"}
	for _, tc := range []struct {
		name      string
		processes []fakeProcess
		want      bool
	}{
		{"live leader", []fakeProcess{{100, 100, "S", 500, 40}}, true},
		{"member after leader exit", []fakeProcess{{230, 100, "S", 900, 40}}, true},
		{"leader in another session", []fakeProcess{{100, 100, "S", 500, 41}}, false},
		{"member in another session", []fakeProcess{{230, 100, "S", 900, 41}}, false},
		{"reused leader ID", []fakeProcess{{100, 100, "S", 7000, 40}}, false},
		{"zombie leader alone", []fakeProcess{{100, 100, "Z", 500, 40}}, false},
		{"member predating the leader", []fakeProcess{{50, 100, "S", 400, 40}}, false},
	} {
		if got := ownedGroupRunning(writeProcTable(t, tc.processes...), ref, 40); got != tc.want {
			t.Errorf("%s: ownedGroupRunning = %t; want %t", tc.name, got, tc.want)
		}
	}
	if ownedGroupRunning(filepath.Join(t.TempDir(), "missing"), ref, 40) {
		t.Error("an unreadable process table verified a running group")
	}
	if ownedGroupRunning(writeProcTable(t, fakeProcess{100, 100, "S", 500, 40}), processRef{PID: 100}, 40) {
		t.Error("a record without a start tick verified a running group")
	}
}
