package manager

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestProductionEffectsWireRealIdentityReads proves that the production effects constructor wires the real group and full ID readers through its function-reference seam without invoking them.
func TestProductionEffectsWireRealIdentityReads(t *testing.T) {
	effects := newEffects(systemCalls{})
	for _, reader := range []struct {
		name      string
		got, want any
	}{
		{"groups", effects.Groups, os.Getgroups},
		{"uids", effects.UIDs, unix.Getresuid},
		{"gids", effects.GIDs, unix.Getresgid},
	} {
		if got, want := reflect.ValueOf(reader.got).Pointer(), reflect.ValueOf(reader.want).Pointer(); got != want {
			t.Errorf("%s reader pointer=%#x; want production reader %#x", reader.name, got, want)
		}
	}
}

func stubPassingAgentIDReads(effects *Effects) {
	effects.Groups = func() ([]int, error) { return nil, nil }
	effects.UIDs = func() (int, int, int) { return 1000, 1000, 1000 }
	effects.GIDs = func() (int, int, int) { return 1000, 1000, 1000 }
}

func assertAgentCredentials(t *testing.T, command *exec.Cmd, noSetGroups bool) {
	t.Helper()
	if command.SysProcAttr == nil || command.SysProcAttr.Credential == nil {
		t.Error("process has no explicit OS credential")
		return
	}
	credential := command.SysProcAttr.Credential
	if credential.Uid != 1000 || credential.Gid != 1000 || credential.NoSetGroups != noSetGroups || len(credential.Groups) != 0 {
		t.Errorf("credential=%+v; want 1000:1000, empty groups, NoSetGroups=%v", credential, noSetGroups)
	}
}

// TestProductionDirectSessionsUseAgentEnvironment proves that direct session queries replace caller home and user variables exactly once and remove caller tmux settings through the production exec.Cmd seam.
func TestProductionDirectSessionsUseAgentEnvironment(t *testing.T) {
	starts := 0
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable: func() (string, error) { t.Error("direct session query tried to re-exec"); return "", nil },
		Environment: func() []string {
			return []string{"HOME=/home/caller", "HOME=/caller/duplicate", "USER=caller-user", "LOGNAME=caller-logname", "TMUX=/caller/socket,2,1", "TMUX_TMPDIR=/caller/tmux", "KEEP=direct-marker"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			wanted := map[string]string{"HOME": "/home/agent", "USER": "agent", "LOGNAME": "agent", "KEEP": "direct-marker"}
			counts := make(map[string]int)
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				counts[key]++
				if key == "TMUX" || key == "TMUX_TMPDIR" {
					t.Errorf("direct session query inherited caller variable %q", entry)
				}
				if expected, ok := wanted[key]; ok && value != expected {
					t.Errorf("environment variable %q; want %s=%s", entry, key, expected)
				}
			}
			for key := range wanted {
				if counts[key] != 1 {
					t.Errorf("environment=%q count(%s)=%d; want exactly one", command.Env, key, counts[key])
				}
			}
			return nil
		}})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"sessions"}, io.Discard, io.Discard, effects); status != 0 || starts != 1 {
		t.Fatalf("status=%d starts=%d; want one direct tmux session query", status, starts)
	}
}

// TestProductionSessionWorkerHasAgentCredentials proves that the production adapter re-execs its trusted executable with 1000:1000 credentials, cleared groups, and agent home through the injected exec.Cmd start seam.
func TestProductionSessionWorkerHasAgentCredentials(t *testing.T) {
	var calls int
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 0, GID: 0} },
		Executable: func() (string, error) { return "/trusted/manager", nil },
		Environment: func() []string {
			return []string{"HOME=/root", "USER=root", "LOGNAME=root", "TMUX=/root/socket,1,0", "TMUX_TMPDIR=/root/tmux", "KEEP=value"}
		},
		Start: func(command *exec.Cmd) error {
			calls++
			if command.Path != "/trusted/manager" || !reflect.DeepEqual(command.Args, []string{"/trusted/manager", "__worker", "sessions"}) {
				t.Errorf("command=%+v", command)
			}
			assertAgentCredentials(t, command, false)
			for _, wanted := range []string{"HOME=/home/agent", "USER=agent", "LOGNAME=agent", "KEEP=value"} {
				found := false
				for _, entry := range command.Env {
					if entry == wanted {
						found = true
					}
				}
				if !found {
					t.Errorf("environment=%q missing %q", command.Env, wanted)
				}
			}
			for _, entry := range command.Env {
				if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_TMPDIR=") || entry == "HOME=/root" || entry == "USER=root" || entry == "LOGNAME=root" {
					t.Errorf("worker inherits root session environment %q", entry)
				}
			}
			return nil
		}})
	if status := RunWithEffects([]string{"sessions"}, io.Discard, io.Discard, effects); status != 0 || calls != 1 {
		t.Fatalf("status=%d starts=%d; want one successful trusted worker", status, calls)
	}
}

// TestProductionMissingProgramReturnsFailure proves that missing tmux propagates lookup failure through the production session query adapter without starting a process.
func TestProductionMissingProgramReturnsFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stderr bytes.Buffer
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable:  func() (string, error) { t.Error("worker session query tried to re-exec"); return "", nil },
		Environment: func() []string { return nil },
		Start: func(command *exec.Cmd) error {
			if !errors.Is(command.Err, exec.ErrNotFound) {
				t.Errorf("lookup error=%v; want missing program", command.Err)
			}
			return command.Err
		}})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"__worker", "sessions"}, io.Discard, &stderr, effects); status != 1 || !strings.Contains(stderr.String(), "tmux") {
		t.Fatalf("status=%d stderr=%q; want failed program lookup", status, stderr.String())
	}
}

// TestProductionExecutableFailureStartsNothing proves that trusted executable lookup failure propagates without a root process fallback through the production re-exec adapter seam.
func TestProductionExecutableFailureStartsNothing(t *testing.T) {
	var stderr bytes.Buffer
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 0, GID: 0} },
		Executable:  func() (string, error) { return "", errors.New("trusted executable unavailable") },
		Environment: func() []string { t.Error("environment read after executable failure"); return nil },
		Start:       func(*exec.Cmd) error { t.Error("process started after executable failure"); return nil }})
	if status := RunWithEffects([]string{"sessions"}, io.Discard, &stderr, effects); status != 1 || !strings.Contains(stderr.String(), "trusted executable unavailable") {
		t.Fatalf("status=%d stderr=%q; want trusted executable failure", status, stderr.String())
	}
}

// TestProductionSessionQueryKeepsAgentCredentials proves that tmux session queries request 1000:1000 without privileged setgroups or caller input through the production exec.Cmd adapter seam.
func TestProductionSessionQueryKeepsAgentCredentials(t *testing.T) {
	var calls int
	effects := newEffects(systemCalls{
		Identity:    func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable:  func() (string, error) { t.Error("worker tried to re-exec"); return "", nil },
		Environment: func() []string { return []string{"HOME=/root", "KEEP=value"} },
		Start: func(command *exec.Cmd) error {
			calls++
			if !reflect.DeepEqual(command.Args, []string{"tmux", "list-sessions", "-F", "#{session_name}"}) {
				t.Errorf("tmux arguments=%q", command.Args)
			}
			assertAgentCredentials(t, command, true)
			if command.Stdin != nil {
				t.Error("tmux session query inherits caller input")
			}
			return nil
		},
	})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"__worker", "sessions"}, io.Discard, io.Discard, effects); status != 0 || calls != 1 {
		t.Fatalf("status=%d starts=%d; want one unprivileged tmux query", status, calls)
	}
}
