package main_test

import (
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// agentStatusPattern reads the host's one-line agents status report.
var agentStatusPattern = regexp.MustCompile(`(?m)^opencode: (not enabled|enabled; version (\S+); pin (\S+); session (true|false); signed in \S+)$`)

// agent enables opencode in a fresh sandbox through a real npm install, starts
// its session through a pseudo-terminal the suite provides, detaches, and
// checks the session, its stop, and the disable through agents status. It
// signs in nowhere and needs the network only for the install.
func (suite *liveSuite) agent(t *testing.T) {
	host := suite.host
	const name, agent = "live-agent", "opencode"
	host.cleanupSandbox(t, name)
	host.mustCLI(t, "", "up", name)
	host.mustCLI(t, "", "agents", "enable", name, agent)
	enabled, version, session := host.agentStatus(t, name, agent)
	if !enabled || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+`).MatchString(version) || session {
		t.Fatalf("after enable: enabled=%v version=%q session=%v; want enabled with an installed version and no session", enabled, version, session)
	}
	terminal := startLiveTerminal(t, host.environment, host.binary, "agents", "session", name, agent)
	defer terminal.Close()
	// Let the session render before detaching: tmux must own the terminal
	// when the prefix arrives, and a quiet screen shows the agent is up.
	waitForTerminalOutput(t, terminal, 3*time.Minute)
	settleTerminalOutput(terminal, 3*time.Second, time.Minute)
	exit, finished := 0, false
	for attempt := 0; attempt < 3 && !finished; attempt++ {
		if err := terminal.Write("\x02d"); err != nil {
			t.Fatalf("send the tmux detach keys: %v", err)
		}
		exit, finished = terminal.Wait(30 * time.Second)
	}
	t.Logf("session terminal output: %q", terminal.Output())
	if !finished {
		t.Fatal("agents session did not return after the detach keys, or something kept its terminal open")
	}
	if exit != 0 {
		t.Fatalf("agents session exited with status %d after detaching", exit)
	}
	if enabled, _, session := host.agentStatus(t, name, agent); !enabled || !session {
		t.Fatalf("after detaching: enabled=%v session=%v; want a running session", enabled, session)
	}
	if output := host.mustCLI(t, "", "agents", "session", name, agent, "--stop"); !strings.Contains(output, "stopped session "+agent) {
		t.Fatalf("session stop did not name the session:\n%s", output)
	}
	if enabled, _, session := host.agentStatus(t, name, agent); !enabled || session {
		t.Fatalf("after stop: enabled=%v session=%v; want no session", enabled, session)
	}
	host.mustCLI(t, "", "agents", "disable", name, agent)
	if enabled, _, _ := host.agentStatus(t, name, agent); enabled {
		t.Fatal("after disable: agent is still enabled")
	}
	host.mustCLI(t, "", "remove", name, "--volumes")
	host.assertNoSandboxObjects(t, name)
}

// agentStatus reads enabled state, installed version, and session from agents status.
func (host liveHost) agentStatus(t *testing.T, name, agent string) (bool, string, bool) {
	t.Helper()
	output := host.mustCLI(t, "", "agents", "status", name, agent)
	match := agentStatusPattern.FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("unreadable agents status:\n%s", output)
	}
	if match[1] == "not enabled" {
		return false, "", false
	}
	return true, match[2], match[4] == "true"
}

// waitForTerminalOutput waits until the command wrote anything at all.
func waitForTerminalOutput(t *testing.T, terminal *liveTerminal, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for terminal.Output() == "" {
		if _, finished := terminal.Wait(0); finished {
			t.Fatalf("command ended before writing to its terminal: %q", terminal.Output())
		}
		if time.Now().After(deadline) {
			t.Fatal("command wrote nothing to its terminal")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// settleTerminalOutput waits until the output stayed unchanged for quiet, or limit passed.
func settleTerminalOutput(terminal *liveTerminal, quiet, limit time.Duration) {
	deadline := time.Now().Add(limit)
	last, since := len(terminal.Output()), time.Now()
	for time.Now().Before(deadline) {
		if _, finished := terminal.Wait(0); finished {
			return
		}
		if current := len(terminal.Output()); current != last {
			last, since = current, time.Now()
		} else if time.Since(since) >= quiet {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestAgentSessionRunsInProvidedTerminal proves the pseudo-terminal the live suite provides counts as an interactive terminal for agents session and carries the session's output and a typed line both ways through the built CLI and fake Podman.
func TestAgentSessionRunsInProvidedTerminal(t *testing.T) {
	responses := ownedLifecycleResponses(true)
	responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
	responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: `{"name":"codex","enabled":true,"installed_version":"1.2.3","signed_in":"unknown","session":false}`}
	attach := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i -t sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager agents session codex")
	responses[strings.Join(attach, " ")] = lifecycleResponse{Output: "attached to codex\n", EchoLine: true}
	f := agentExecutableHarness(t, responses)
	terminal := startLiveTerminal(t, os.Environ(), executable, "agents", "session", "demo", "codex")
	defer terminal.Close()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(terminal.Output(), "attached to codex") {
		if _, finished := terminal.Wait(0); finished || time.Now().After(deadline) {
			t.Fatalf("session output never reached the terminal: finished=%v output=%q", finished, terminal.Output())
		}
		time.Sleep(50 * time.Millisecond)
	}
	enter := "\n"
	if runtime.GOOS == "windows" {
		enter = "\r"
	}
	if err := terminal.Write("detach" + enter); err != nil {
		t.Fatal(err)
	}
	exit, finished := terminal.Wait(20 * time.Second)
	if !finished || exit != 0 || !strings.Contains(terminal.Output(), "line: detach") {
		t.Fatalf("finished=%v exit=%d output=%q; want exit 0 with the typed line echoed by the session", finished, exit, terminal.Output())
	}
	calls := agentManagerCalls(t, f)
	if len(calls) == 0 || !reflect.DeepEqual(calls[len(calls)-1], attach) {
		t.Fatalf("calls=%v; want final interactive attach %v", calls, attach)
	}
}
