package manager_test

import (
	"bytes"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestAgentRunForwardsArgumentsStreamsAndExit proves one-shot agent execution preserves arguments, streams and status at RunWithEffects and its process seam.
func TestAgentRunForwardsArgumentsStreamsAndExit(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	e.Stdin = strings.NewReader("input")
	e.Start = func(p manager.Process) error {
		if p.Directory != "/workspace" {
			t.Fatalf("directory=%q", p.Directory)
		}
		if p.Program != filepath.Join(home, ".local", "bin", "codex") || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !reflect.DeepEqual(p.Args, []string{"--flag", "a b", "$(literal)", "--"}) {
			t.Fatalf("process=%+v", p)
		}
		input, _ := io.ReadAll(p.Stdin)
		if string(input) != "input" {
			t.Fatalf("stdin=%q", input)
		}
		io.WriteString(p.Stdout, "agent output")
		io.WriteString(p.Stderr, "agent diagnostic")
		return childExit(17)
	}
	var output, diagnostic bytes.Buffer
	status := manager.RunWithEffects([]string{"agents", "run", "codex", "--flag", "a b", "$(literal)", "--"}, &output, &diagnostic, e)
	if status != 17 || output.String() != "agent output" || diagnostic.String() != "agent diagnostic" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, &output, &diagnostic)
	}

}

// TestAgentSessionStartsOnceAndAttachesWithoutLock proves repeated clients share one session and attachment releases the manager lock at RunWithEffects and its process seam.
func TestAgentSessionStartsOnceAndAttachesWithoutLock(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	running, starts, attaches := false, 0, 0
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !reflect.DeepEqual(p.Args[:4], []string{"-L", "sandboxed-agents", "-f", "/dev/null"}) {
			t.Fatalf("process=%+v", p)
		}
		switch p.Args[4] {
		case "list-sessions":
			if running {
				io.WriteString(p.Stdout, "codex\n")
			}
		case "new-session":
			want := []string{"new-session", "-d", "-s", "codex", "-c", "/workspace", "--", "/usr/bin/env", filepath.Join(home, ".local", "bin", "codex")}
			if !reflect.DeepEqual(p.Args[4:], want) {
				t.Fatalf("creation=%q", p.Args)
			}
			starts++
			running = true
		case "attach-session":
			if !reflect.DeepEqual(p.Args[4:], []string{"attach-session", "-t", "=codex"}) {
				t.Fatal(p.Args)
			}
			attaches++
			// Same-version pinning must acquire the lock even while this client is attached.
			runAgents(t, e, 0, "enable", "codex", "--version", "1.2.3")
		default:
			t.Fatalf("unexpected tmux command: %q", p.Args)
		}
		return nil
	}
	runAgents(t, e, 0, "session", "codex")
	runAgents(t, e, 0, "session", "codex")
	if starts != 1 || attaches != 2 || installs != 1 {
		t.Fatalf("starts=%d attaches=%d installs=%d", starts, attaches, installs)
	}
}

// TestAgentInstallationGuardsItsOwnSession proves replacement and disable refuse or force-stop only the affected agent at RunWithEffects and the process/filesystem seams.
func TestAgentInstallationGuardsItsOwnSession(t *testing.T) {
	for _, operation := range [][]string{{"disable", "codex"}, {"update", "codex"}, {"enable", "codex", "--version", "2.0.0"}} {
		t.Run(strings.Join(operation, " "), func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.2.3", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex")
			npm := e.Start
			running, stops := true, 0
			e.Start = func(p manager.Process) error {
				if p.Program != "tmux" {
					if running {
						t.Fatal("installation changed before stopping session")
					}
					return npm(p)
				}
				switch p.Args[4] {
				case "list-sessions":
					io.WriteString(p.Stdout, "claude\n")
					if running {
						io.WriteString(p.Stdout, "codex\n")
					}
				case "kill-session":
					if !reflect.DeepEqual(p.Args[4:], []string{"kill-session", "-t", "=codex"}) {
						t.Fatal(p.Args)
					}
					running = false
					stops++
				default:
					t.Fatalf("tmux=%q", p.Args)
				}
				return nil
			}
			output := runAgents(t, e, 1, operation...)
			if !strings.Contains(output, "codex") || !strings.Contains(output, "--force") || installs != 1 || stops != 0 {
				t.Fatalf("refusal=%s installs=%d stops=%d", output, installs, stops)
			}
			output = runAgents(t, e, 0, append(append([]string{}, operation...), "--force")...)
			if !strings.Contains(output, "stopped session codex") || stops != 1 {
				t.Fatalf("force=%s stops=%d", output, stops)
			}
		})
	}
}

// TestAgentStatusReportsItsSession proves status distinguishes the requested agent's running session from another agent through RunWithEffects.
func TestAgentStatusReportsItsSession(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	for _, tc := range []struct {
		names string
		want  bool
	}{{"", false}, {"claude\n", false}, {"claude\ncodex\nopencode\n", true}} {
		e.Start = func(p manager.Process) error {
			if p.Program != "tmux" {
				t.Fatalf("unexpected process=%+v", p)
			}
			io.WriteString(p.Stdout, tc.names)
			return nil
		}
		if status := readAgentStatus(t, e, "codex"); status.Session != tc.want {
			t.Fatalf("sessions=%q status=%+v", tc.names, status)
		}
	}
}

// TestAgentLoginRunsSelectedWorkflow proves login prints guidance, uses the catalog workflow and normalizes failure at RunWithEffects and the process seam.
func TestAgentLoginRunsSelectedWorkflow(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	for _, tc := range []struct {
		workflow string
		args     []string
		failure  error
		status   int
	}{
		{"browser", []string{"login"}, nil, 0}, {"device-code", []string{"login", "--device-auth"}, childExit(17), 1},
	} {
		e.Start = func(p manager.Process) error {
			if p.Directory != "/workspace" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || p.Program != filepath.Join(home, ".local", "bin", "codex") || !reflect.DeepEqual(p.Args, tc.args) {
				t.Fatalf("process=%+v", p)
			}
			io.WriteString(p.Stdout, "workflow output")
			return tc.failure
		}
		output := runAgents(t, e, tc.status, "login", "codex", tc.workflow)
		if !strings.Contains(output, "workflow output") || len(output) <= len("workflow output") {
			t.Fatalf("output=%s", output)
		}
	}
}

// TestAgentSessionStopIsIdempotent proves stop ends only the named session and accepts absent or disabled agents at RunWithEffects.
func TestAgentSessionStopIsIdempotent(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "session", "codex", "--stop")
	runAgents(t, e, 0, "enable", "codex")
	running, stops := true, 0
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" {
			t.Fatalf("process=%+v", p)
		}
		switch p.Args[4] {
		case "list-sessions":
			io.WriteString(p.Stdout, "claude\n")
			if running {
				io.WriteString(p.Stdout, "codex\n")
			}
		case "kill-session":
			if p.Args[len(p.Args)-1] != "=codex" {
				t.Fatal(p.Args)
			}
			running = false
			stops++
		default:
			t.Fatal(p.Args)
		}
		return nil
	}
	if output := runAgents(t, e, 0, "session", "codex", "--stop"); !strings.Contains(output, "stopped session codex") {
		t.Fatal(output)
	}
	runAgents(t, e, 0, "session", "codex", "--stop")
	if running || stops != 1 {
		t.Fatalf("running=%t stops=%d", running, stops)
	}
}

// TestAgentWorkRefusesRootAndDisabledSelection proves direct work never reexecs root and disabled agents fail before starting a process at RunWithEffects.
func TestAgentWorkRefusesRootAndDisabledSelection(t *testing.T) {
	for _, args := range [][]string{{"run", "codex"}, {"login", "codex", "browser"}, {"session", "codex"}} {
		root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
		root.HomeDirectory = func() (string, error) { t.Error("root read home"); return "", nil }
		runAgents(t, root, 1, args...)
		e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
		e.HomeDirectory = func() (string, error) { return t.TempDir(), nil }
		if output := runAgents(t, e, 1, args...); !strings.Contains(output, "not enabled") || !strings.Contains(output, "agents enable NAME codex") {
			t.Fatal(output)
		}
	}
}

// TestAgentSameVersionAndUnrelatedSessionsDoNotBlock proves no-op enable preserves sessions and another agent's session never blocks installation at RunWithEffects.
func TestAgentSameVersionAndUnrelatedSessionsDoNotBlock(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	npm := e.Start
	e.Start = func(p manager.Process) error {
		if p.Program == "tmux" {
			if p.Args[4] != "list-sessions" {
				t.Fatalf("changed session: %q", p.Args)
			}
			io.WriteString(p.Stdout, "codex\nclaude\n")
			return nil
		}
		return npm(p)
	}
	for _, args := range [][]string{{"enable", "codex"}, {"enable", "codex", "--force"}, {"enable", "codex", "--version", "1.2.3"}, {"enable", "codex", "--version", "1.2.3", "--force"}, {"enable", "opencode", "--force"}} {
		runAgents(t, e, 0, args...)
	}
	if status := readAgentStatus(t, e, "codex"); status.Pin != "1.2.3" || !status.Session || installs != 2 {
		t.Fatalf("status=%+v installs=%d", status, installs)
	}
	runAgents(t, e, 0, "update", "opencode")
	runAgents(t, e, 0, "disable", "opencode")
}

// TestAgentSessionQueryFailurePreventsChanges proves an unknown session state blocks installation and session changes even with force at RunWithEffects.
func TestAgentSessionQueryFailurePreventsChanges(t *testing.T) {
	for _, args := range [][]string{{"disable", "codex", "--force"}, {"update", "codex", "--force"}, {"enable", "codex", "--version", "2.0.0", "--force"}, {"session", "codex"}, {"session", "codex", "--stop"}} {
		home, version, installs := t.TempDir(), "1.2.3", 0
		e := agentEffects(t, home, &version, &installs)
		runAgents(t, e, 0, "enable", "codex")
		e.Start = func(p manager.Process) error {
			if p.Program != "tmux" || p.Args[4] != "list-sessions" {
				t.Fatalf("changed before query: %+v", p)
			}
			io.WriteString(p.Stderr, "permission denied")
			return childExit(1)
		}
		if output := runAgents(t, e, 1, args...); !strings.Contains(output, "cannot query agent sessions") {
			t.Fatal(output)
		}
	}
}
