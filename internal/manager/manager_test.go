package manager_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

var sessionRequests = [][]string{{"sessions"}, {"__worker", "sessions"}}

func noProcesses(t *testing.T, identity manager.Identity) manager.Effects {
	t.Helper()
	return manager.Effects{
		Identity: func() manager.Identity { return identity },
		Groups:   func() ([]int, error) { return nil, nil },
		UIDs:     func() (int, int, int) { return identity.UID, identity.UID, identity.UID },
		GIDs:     func() (int, int, int) { return identity.GID, identity.GID, identity.GID },
		Start:    func(manager.Process) error { t.Error("unexpected program start"); return nil },
		Reexec:   func(manager.Process) error { t.Error("unexpected reexec"); return nil },
	}
}

func rootToWorker(t *testing.T, worker manager.Effects, events *[]string) manager.Effects {
	t.Helper()
	workerArgs := []string{"__worker", "sessions"}
	root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	root.Reexec = func(process manager.Process) error {
		*events = append(*events, "worker")
		if process.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !reflect.DeepEqual(process.Args, workerArgs) {
			t.Errorf("worker request=%+v; want args=%q and 1000:1000", process, workerArgs)
		}
		if status := manager.RunWithEffects(process.Args, process.Stdout, process.Stderr, worker); status != 0 {
			t.Errorf("worker status=%d", status)
		}
		return nil
	}
	return root
}

// TestDirectSessionsStartTmuxWithAgentIdentity proves that public sessions under verified agent IDs start exactly one tmux query as 1000:1000 without re-exec through the manager command and injected process seam.
func TestDirectSessionsStartTmuxWithAgentIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var started []manager.Process
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	effects.Start = func(process manager.Process) error { started = append(started, process); return nil }
	status := manager.RunWithEffects([]string{"sessions"}, &stdout, &stderr, effects)
	if status != 0 || len(started) != 1 || stderr.Len() != 0 {
		t.Fatalf("status=%d starts=%d stderr=%q; want one successful direct session query", status, len(started), stderr.String())
	}
	process := started[0]
	if process.Program != "tmux" || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !reflect.DeepEqual(process.Args, []string{"list-sessions", "-F", "#{session_name}"}) {
		t.Fatalf("process=%+v; want tmux list-sessions -F #{session_name} as 1000:1000", process)
	}
}

// TestAgentWorkReadsFullIdentityOnce proves that direct and worker commands read each full ID set once and query effective identity only for public session queries through the manager command and injected read seams.
func TestAgentWorkReadsFullIdentityOnce(t *testing.T) {
	for _, args := range sessionRequests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			identityReads, uidReads, gidReads, starts := 0, 0, 0, 0
			effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			effects.Identity = func() manager.Identity {
				identityReads++
				return manager.Identity{UID: 1000, GID: 1000}
			}
			effects.UIDs = func() (int, int, int) { uidReads++; return 1000, 1000, 1000 }
			effects.GIDs = func() (int, int, int) { gidReads++; return 1000, 1000, 1000 }
			effects.Start = func(manager.Process) error { starts++; return nil }
			wantIdentityReads := 0
			if len(args) == 1 {
				wantIdentityReads = 1
			}
			status := manager.RunWithEffects(args, &stdout, &stderr, effects)
			if status != 0 || starts != 1 || identityReads != wantIdentityReads || uidReads != 1 || gidReads != 1 || stderr.Len() != 0 {
				t.Fatalf("status=%d starts=%d effective reads=%d UID reads=%d GID reads=%d stderr=%q; want one full identity check", status, starts, identityReads, uidReads, gidReads, stderr.String())
			}
		})
	}
}

// TestRootAdministrativeRequestDefersAgentChecksToWorker proves that a root session query re-execs its worker before agent checks and refuses unsafe worker groups before tmux through the manager command and injected read seams.
func TestRootAdministrativeRequestDefersAgentChecksToWorker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	root.Groups = func() ([]int, error) { t.Error("root administrative request read agent groups"); return []int{0}, nil }
	root.UIDs = func() (int, int, int) { t.Error("root administrative request read agent UIDs"); return 0, 0, 0 }
	root.GIDs = func() (int, int, int) { t.Error("root administrative request read agent GIDs"); return 0, 0, 0 }
	workers := 0
	root.Reexec = func(process manager.Process) error {
		workers++
		worker := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
		worker.Groups = func() ([]int, error) { return []int{0}, nil }
		return childExit(manager.RunWithEffects(process.Args, process.Stdout, process.Stderr, worker))
	}
	if status := manager.RunWithEffects([]string{"sessions"}, &stdout, &stderr, root); status != 1 || workers != 1 || !strings.Contains(stderr.String(), "supplementary groups") {
		t.Errorf("status=%d workers=%d stderr=%q; want unsafe worker refusal after re-exec", status, workers, stderr.String())
	}
}

// TestAgentWorkRequiresAllRealEffectiveAndSavedIDs proves that every UID and GID must be 1000 and missing reads fail closed through the manager command and injected identity-read seams.
func TestAgentWorkRequiresAllRealEffectiveAndSavedIDs(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		uids, gids               [3]int
		missingUIDs, missingGIDs bool
	}{
		{"real-uid", [3]int{0, 1000, 1000}, [3]int{1000, 1000, 1000}, false, false},
		{"effective-uid", [3]int{1000, 0, 1000}, [3]int{1000, 1000, 1000}, false, false},
		{"saved-uid", [3]int{1000, 1000, 0}, [3]int{1000, 1000, 1000}, false, false},
		{"real-gid", [3]int{1000, 1000, 1000}, [3]int{0, 1000, 1000}, false, false},
		{"effective-gid", [3]int{1000, 1000, 1000}, [3]int{1000, 0, 1000}, false, false},
		{"saved-gid", [3]int{1000, 1000, 1000}, [3]int{1000, 1000, 0}, false, false},
		{"missing-uids", [3]int{1000, 1000, 1000}, [3]int{1000, 1000, 1000}, true, false},
		{"missing-gids", [3]int{1000, 1000, 1000}, [3]int{1000, 1000, 1000}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range sessionRequests {
				var stdout, stderr bytes.Buffer
				effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
				effects.UIDs = func() (int, int, int) { return tc.uids[0], tc.uids[1], tc.uids[2] }
				effects.GIDs = func() (int, int, int) { return tc.gids[0], tc.gids[1], tc.gids[2] }
				if tc.missingUIDs {
					effects.UIDs = nil
				}
				if tc.missingGIDs {
					effects.GIDs = nil
				}
				status := manager.RunWithEffects(args, &stdout, &stderr, effects)
				if status != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "real, effective, and saved UID and GID 1000") {
					t.Errorf("args=%q status=%d stdout=%q stderr=%q; want full identity refusal", args, status, stdout.String(), stderr.String())
				}
			}
		})
	}
}

// TestAgentWorkRequiresSafeSupplementaryGroups proves that only empty groups or exactly group 1000 can reach a program through the manager command and injected group-read seams.
func TestAgentWorkRequiresSafeSupplementaryGroups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		groups  []int
		err     error
		absent  bool
		allowed bool
	}{
		{"empty", nil, nil, false, true},
		{"agent", []int{1000}, nil, false, true},
		{"root", []int{0}, nil, false, false},
		{"agent-and-root", []int{1000, 0}, nil, false, false},
		{"duplicate-agent", []int{1000, 1000}, nil, false, false},
		{"query-error", nil, errors.New("group lookup failed"), false, false},
		{"missing-query", nil, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range sessionRequests {
				var stdout, stderr bytes.Buffer
				starts := 0
				effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
				effects.Groups = func() ([]int, error) { return tc.groups, tc.err }
				if tc.absent {
					effects.Groups = nil
				}
				effects.Start = func(manager.Process) error { starts++; return nil }
				status := manager.RunWithEffects(args, &stdout, &stderr, effects)
				if tc.allowed {
					if status != 0 || starts != 1 || stderr.Len() != 0 {
						t.Errorf("args=%q status=%d starts=%d stderr=%q; want allowed groups", args, status, starts, stderr.String())
					}
				} else if status != 1 || starts != 0 || !strings.Contains(stderr.String(), "supplementary groups") {
					t.Errorf("args=%q status=%d starts=%d stderr=%q; want group refusal before process start", args, status, starts, stderr.String())
				}
			}
		})
	}
}

type childExit int

func (exit childExit) Error() string { return "scripted child failure" }
func (exit childExit) ExitCode() int { return int(exit) }

// TestSessionsReportsNoTmuxServerAsEmptyList proves that exit 1 with "no server running" or "error connecting to" followed by "(No such file or directory)" yields an empty list,
// while "Permission denied", "Not a directory", unrelated errors, other exit codes, and output remain observable through the manager command and injected process seam.
func TestSessionsReportsNoTmuxServerAsEmptyList(t *testing.T) {
	for _, tc := range []struct {
		name, message, output string
		err                   error
		status                int
		stderr                string
	}{
		{"no-server-running", "no server running on /tmp/tmux-1000/default\n", "", childExit(1), 0, ""},
		{"error-connecting", "error connecting to /tmp/tmux-1000/default (No such file or directory)\n", "", childExit(1), 0, ""},
		{"permission-denied", "error connecting to /tmp/tmux-1000/default (Permission denied)\n", "", childExit(1), 1, "error connecting to /tmp/tmux-1000/default (Permission denied)\nscripted child failure\n"},
		{"not-a-directory", "error connecting to /tmp/tmux-1000/default (Not a directory)\n", "", childExit(1), 1, "error connecting to /tmp/tmux-1000/default (Not a directory)\nscripted child failure\n"},
		{"unrelated-exit-one", "tmux: permission denied\n", "", childExit(1), 1, "tmux: permission denied\nscripted child failure\n"},
		{"other-exit-code", "no server running\n", "", childExit(23), 23, "no server running\nscripted child failure\n"},
		{"successful-output", "no server running\n", "existing-session\n", nil, 0, "no server running\n"},
		{"non-exit-error", "error connecting to socket\n", "", errors.New("cannot start tmux"), 1, "error connecting to socket\ncannot start tmux\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range sessionRequests {
				var stdout, stderr bytes.Buffer
				starts := 0
				effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
				effects.Start = func(process manager.Process) error {
					starts++
					process.Stdout.Write([]byte(tc.output))
					process.Stderr.Write([]byte(tc.message))
					return tc.err
				}
				status := manager.RunWithEffects(args, &stdout, &stderr, effects)
				if status != tc.status || starts != 1 || stdout.String() != tc.output || stderr.String() != tc.stderr {
					t.Errorf("args=%q status=%d starts=%d stdout=%q stderr=%q; want status=%d stdout=%q stderr=%q", args, status, starts, stdout.String(), stderr.String(), tc.status, tc.output, tc.stderr)
				}
			}
		})
	}
}

// TestProcessFailuresPropagateWithoutFallback proves that start and re-exec failures preserve child exit status or return failure without an alternate process through the manager command seam.
func TestProcessFailuresPropagateWithoutFallback(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		identity manager.Identity
		reexec   bool
	}{
		{[]string{"sessions"}, manager.Identity{UID: 0, GID: 0}, true},
		{[]string{"sessions"}, manager.Identity{UID: 1000, GID: 1000}, false},
		{[]string{"__worker", "sessions"}, manager.Identity{UID: 1000, GID: 1000}, false},
	} {
		for _, failure := range []struct {
			err    error
			status int
		}{{errors.New("program unavailable"), 1}, {childExit(23), 23}, {childExit(-1), 1}} {
			var stdout, stderr bytes.Buffer
			calls := 0
			effects := noProcesses(t, tc.identity)
			fail := func(manager.Process) error { calls++; return failure.err }
			if tc.reexec {
				effects.Reexec = fail
			} else {
				effects.Start = fail
			}
			status := manager.RunWithEffects(tc.args, &stdout, &stderr, effects)
			if status != failure.status || calls != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), failure.err.Error()) {
				t.Errorf("args=%q status=%d calls=%d stdout=%q stderr=%q; want failure=%v status=%d", tc.args, status, calls, stdout.String(), stderr.String(), failure.err, failure.status)
			}
		}
	}
}

// TestMalformedManagerRequestsNeverStartPrograms proves that incomplete and unknown command forms are usage errors before any process through the manager command seam.
func TestMalformedManagerRequestsNeverStartPrograms(t *testing.T) {
	for _, args := range [][]string{
		{}, {"version", "extra"}, {"sessions", "extra"}, {"unknown"},
		{"__worker"}, {"__worker", "unknown"}, {"__worker", "sessions", "extra"},
	} {
		for _, identity := range []manager.Identity{{UID: 0, GID: 0}, {UID: 1000, GID: 1000}} {
			var stdout, stderr bytes.Buffer
			status := manager.RunWithEffects(args, &stdout, &stderr, noProcesses(t, identity))
			if status != 2 || stdout.Len() != 0 || stderr.String() != "usage: sandboxed-agents-manager version | sessions | ssh-host-key | ssh-authorize KEY | ssh-deauthorize | integrations config git credentials | integrations config git identity --name NAME --email EMAIL | integrations login azdo --organization URL | integrations login github device-code\n" {
				t.Errorf("args=%q identity=%+v status=%d stdout=%q stderr=%q; want usage error", args, identity, status, stdout.String(), stderr.String())
			}
		}
	}
}

// TestAgentSessionQueryRefusesOtherIdentities proves that session queries reject mismatched agent IDs and root workers before any program or re-exec through the manager command seam.
func TestAgentSessionQueryRefusesOtherIdentities(t *testing.T) {
	for _, identity := range []manager.Identity{
		{UID: 0, GID: 0}, {UID: 0, GID: 1000}, {UID: 1000, GID: 0},
		{UID: 1001, GID: 1000}, {UID: 1000, GID: 1001}, {UID: 1001, GID: 1001},
	} {
		for _, args := range sessionRequests {
			if identity == (manager.Identity{UID: 0, GID: 0}) && len(args) == 1 {
				continue
			}
			var stdout, stderr bytes.Buffer
			status := manager.RunWithEffects(args, &stdout, &stderr, noProcesses(t, identity))
			if status != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "requires real, effective, and saved UID and GID 1000") {
				t.Errorf("identity=%+v args=%q status=%d stdout=%q stderr=%q; want identity refusal", identity, args, status, stdout.String(), stderr.String())
			}
		}
	}
}

// TestRootSessionQueryReexecutesBeforeTmux proves that the manager command changes root to a 1000:1000 worker before querying agent sessions at the injected process seam.
func TestRootSessionQueryReexecutesBeforeTmux(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var events []string
	worker := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	worker.Start = func(process manager.Process) error {
		events = append(events, "tmux")
		if process.Identity != (manager.Identity{UID: 1000, GID: 1000}) || process.Program != "tmux" || !reflect.DeepEqual(process.Args, []string{"list-sessions", "-F", "#{session_name}"}) {
			t.Errorf("session query process=%+v", process)
		}
		process.Stdout.Write([]byte("codex-session\n"))
		return nil
	}
	root := rootToWorker(t, worker, &events)
	status := manager.RunWithEffects([]string{"sessions"}, &stdout, &stderr, root)
	if status != 0 || stdout.String() != "codex-session\n" || stderr.Len() != 0 || !reflect.DeepEqual(events, []string{"worker", "tmux"}) {
		t.Fatalf("status=%d events=%v stdout=%q stderr=%q", status, events, stdout.String(), stderr.String())
	}
}

// TestRootVersionAnswersWithoutStartingPrograms proves that root obtains the manager version through its command seam without re-exec or agent work.
func TestRootVersionAnswersWithoutStartingPrograms(t *testing.T) {
	var stdout, stderr bytes.Buffer
	effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	effects.Identity = func() manager.Identity { t.Error("version queried process identity"); return manager.Identity{} }
	effects.Groups, effects.UIDs, effects.GIDs = nil, nil, nil
	status := manager.RunWithEffects([]string{"version"}, &stdout, &stderr, effects)
	if status != 0 || stdout.String() != "sandboxed-agents-manager development\n" || stderr.Len() != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q; want successful version", status, stdout.String(), stderr.String())
	}
}
