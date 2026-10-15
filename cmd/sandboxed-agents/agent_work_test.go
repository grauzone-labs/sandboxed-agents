package main_test

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestAgentRunForwardsLiteralArguments proves dash-prefixed, empty, and shell-like arguments and the agent exit status survive the executable CLI and fake Podman seam.
func TestAgentRunForwardsLiteralArguments(t *testing.T) {
	for _, exit := range []int{0, 23, 127} {
		responses := ownedLifecycleResponses(true)
		responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
		responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: `{"name":"codex","enabled":true,"installed_version":"1.2.3"}`}
		forwarded := []string{"--unknown-to-host", "two words", "", "$(touch never)", "--", "-x"}
		execution := append([]string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", "-i", "sandboxed-agents.default.demo", "/usr/local/bin/sandboxed-agents-manager", "agents", "run", "codex"}, forwarded...)
		responses[strings.Join(execution, " ")] = lifecycleResponse{Output: "agent output\n", Stderr: "agent diagnostic\n", Exit: exit}
		f := agentExecutableHarness(t, responses)
		command := exec.Command(executable, append([]string{"agents", "run", "demo", "codex"}, forwarded...)...)
		command.Env = os.Environ()
		output, err := command.CombinedOutput()
		if status := exitStatus(t, err); status != exit || string(output) != "agent output\nagent diagnostic\n" {
			t.Fatalf("status=%d output=%q; want status=%d", status, output, exit)
		}
		calls := agentManagerCalls(t, f)
		if len(calls) != 3 || !reflect.DeepEqual(calls[2], execution) {
			t.Fatalf("calls=%v; want final=%v", calls, execution)
		}
	}
}

// TestAgentLoginWorkflowUsage proves missing multi-workflow and unknown login names list valid choices before Podman at the executable CLI seam.
func TestAgentLoginWorkflowUsage(t *testing.T) {
	for _, args := range [][]string{{"codex"}, {"codex", "unknown"}, {"claude", "unknown"}, {"copilot", "unknown"}, {"opencode", "unknown"}} {
		f := testutil.Install(t, fakeExecutable)
		command := exec.Command(executable, append([]string{"agents", "login", "demo"}, args...)...)
		command.Env = f.Env(os.Environ())
		output, err := command.CombinedOutput()
		choices := map[string]string{"codex": "browser, device-code", "claude": "browser", "copilot": "device-code", "opencode": "provider"}
		if status := exitStatus(t, err); status != 2 || !strings.Contains(string(output), "valid names: "+choices[args[0]]) {
			t.Fatalf("args=%v status=%d output=%s", args, status, output)
		}
		assertNoExternalCalls(t, f)
	}
}

// TestAgentSessionStopNeedsNoTerminal proves stopping an agent session dispatches without a terminal even for a disabled agent through the executable CLI and fake Podman.
func TestAgentSessionStopNeedsNoTerminal(t *testing.T) {
	for _, answer := range []string{`{"name":"codex","enabled":false}`, `{"name":"codex","enabled":true,"installed_version":"1.2.3","session":true}`} {
		responses := ownedLifecycleResponses(true)
		responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
		responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: answer}
		execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager agents session codex --stop")
		responses[strings.Join(execution, " ")] = lifecycleResponse{Output: "codex: no running session; nothing to stop\n"}
		f := agentExecutableHarness(t, responses)
		status, output := runAgentExecutable(t, "agents", "session", "demo", "codex", "--stop")
		if status != 0 || !strings.Contains(output, "nothing to stop") {
			t.Fatalf("status=%d output=%s", status, output)
		}
		calls := agentManagerCalls(t, f)
		if len(calls) != 3 || !reflect.DeepEqual(calls[2], execution) {
			t.Fatalf("calls=%v want final=%v", calls, execution)
		}
	}
}

// TestDisabledAgentWorkRefusesBeforeTerminal proves run, login, session, and update name enable before terminal checks while status, disable, and session stop succeed at the executable CLI seam.
func TestDisabledAgentWorkRefusesBeforeTerminal(t *testing.T) {
	for _, verb := range []string{"run", "login", "session", "update", "status", "disable", "session-stop"} {
		responses := ownedLifecycleResponses(true)
		responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
		responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: `{"name":"codex","enabled":false}`}
		responses[agentManagerPrefix+"agents disable codex"] = lifecycleResponse{Output: "codex: not enabled; nothing to do\n"}
		responses["exec --user=1000:1000 --env HOME=/home/agent -i sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager agents session codex --stop"] = lifecycleResponse{Output: "codex: no running session; nothing to stop\n"}
		f := agentExecutableHarness(t, responses)
		args := agentVerbArgs(verb)
		status, output := runAgentExecutable(t, args...)
		if verb == "status" || verb == "disable" || verb == "session-stop" {
			if status != 0 {
				t.Fatalf("verb=%s status=%d output=%s", verb, status, output)
			}
		} else {
			if status != 1 || !strings.Contains(output, "agents enable demo codex") || strings.Contains(output, "terminal") {
				t.Fatalf("verb=%s status=%d output=%s", verb, status, output)
			}
			if calls := agentManagerCalls(t, f); len(calls) != 2 {
				t.Fatalf("work attempted before disabled-agent refusal: %v", calls)
			}
		}
	}
}
