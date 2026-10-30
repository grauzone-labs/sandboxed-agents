package manager_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestAgentSessionLifetimeWithLocalTmux proves a detached session survives its caller and vanishes with the agent through RunWithEffects and a real local tmux process adapter.
func TestAgentSessionLifetimeWithLocalTmux(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	tmuxPath, localArgs := localTmux(t, home)
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	// The managed agent is a tiny process that exits when its input file appears.
	script := "#!/bin/sh\nwhile [ ! -f finish ]; do sleep 0.02; done\n"
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	attaches := 0
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("process=%+v", p)
		}
		if tmuxCommand(p.Args) == "attach-session" {
			attaches++
			return nil
		} // Model terminal detach at the process seam.
		cmd := exec.Command(tmuxPath, localArgs(p.Args)...)
		cmd.Env = append(os.Environ(), "TMUX=", "TMUX_TMPDIR=")
		cmd.Stdout, cmd.Stderr = p.Stdout, p.Stderr
		return cmd.Run()
	}
	runAgents(t, e, 0, "session", "codex")
	runAgents(t, e, 0, "session", "codex")
	if attaches != 2 || !readAgentStatus(t, e, "codex").Session {
		t.Fatalf("attaches=%d; detached agent session missing", attaches)
	}
	var out, diagnostic bytes.Buffer
	if status := manager.RunWithEffects([]string{"sessions"}, &out, &diagnostic, e); status != 0 || out.String() != "codex\n" {
		t.Fatalf("sessions status=%d output=%s diagnostic=%s", status, &out, &diagnostic)
	}
	if err := os.WriteFile(filepath.Join(home, "finish"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out.Reset()
		diagnostic.Reset()
		status := manager.RunWithEffects([]string{"agents", "status", "codex"}, &out, &diagnostic, e)
		var answer manager.AgentStatus
		if status == 0 && json.Unmarshal(out.Bytes(), &answer) == nil && !answer.Session {
			break
		}
		// tmux may refuse the connection while its last session and server exit.
		if status != 0 && !strings.Contains(diagnostic.String(), "server exited unexpectedly") {
			t.Fatalf("status=%d: %s", status, &diagnostic)
		}
		if time.Now().After(deadline) {
			t.Fatalf("session remained after agent exit: %s %s", &out, &diagnostic)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if output := runAgents(t, e, 0, "session", "codex", "--stop"); !strings.Contains(output, "nothing to stop") {
		t.Fatal(output)
	}
	out.Reset()
	if status := manager.RunWithEffects([]string{"sessions"}, &out, io.Discard, e); status != 0 || out.Len() != 0 {
		t.Fatalf("empty query=%q status=%d", &out, status)
	}
}

// localTmux returns the local tmux executable and a rewrite of manager tmux arguments onto a private socket that the
// test's cleanup kills, with the sandbox's /workspace replaced by the given home. Without a local tmux the test skips.
func localTmux(t *testing.T, home string) (string, func(managerArgs []string) []string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("local tmux unavailable; injected process tests cover the manager contract")
	}
	// Keep socket path short enough for sockaddr_un regardless of the test root.
	socketDir, err := os.MkdirTemp("/tmp", "sa-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "socket")
	t.Cleanup(func() {
		exec.Command(tmuxPath, "-S", socket, "kill-server").Run()
		os.RemoveAll(socketDir)
	})
	return tmuxPath, func(managerArgs []string) []string {
		args := append([]string{}, managerArgs...)
		args[0], args[1] = "-S", socket
		for i, arg := range args {
			if arg == "/workspace" {
				args[i] = home
			}
		}
		return args
	}
}
