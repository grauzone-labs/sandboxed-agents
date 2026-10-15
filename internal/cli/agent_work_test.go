package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const agentWorkProbe = "exec --user=0:0 " + shellContainer + " " + integrationManager + " version"
const agentWorkStatus = "exec --user=0:0 " + shellContainer + " " + integrationManager + " agents status "

// TestAgentLoginUsesTerminalAndResolvesWorkflow proves single and explicit login workflows run as agent and normalize workflow failures through RunWithHost and fake Podman.
func TestAgentLoginUsesTerminalAndResolvesWorkflow(t *testing.T) {
	for _, tc := range []struct {
		agent, supplied, resolved string
	}{
		{"copilot", "", "device-code"}, {"claude", "", "browser"}, {"opencode", "", "provider"},
		{"codex", "browser", "browser"}, {"codex", "device-code", "device-code"},
	} {
		for _, exit := range []int{0, 23} {
			f := sandboxStateFixture(t)
			f.host.IsInputTerminal = func(io.Reader) bool { return true }
			f.host.IsOutputTerminal = func(io.Writer) bool { return true }
			f.host.Stdin = strings.NewReader("login input\n")
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[agentWorkStatus+tc.agent] = response{Output: `{"name":"` + tc.agent + `","enabled":true,"installed_version":"1.2.3"}`}
			execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i -t " + shellContainer + " " + integrationManager + " agents login " + tc.agent + " " + tc.resolved)
			f.responses[strings.Join(execution, " ")] = response{Output: "workflow output\n", Exit: exit, EchoStdin: true}
			args := []string{"agents", "login", "agent01", tc.agent}
			if tc.supplied != "" {
				args = append(args, tc.supplied)
			}
			status, output, calls := f.run(t, args...)
			wantStatus := 0
			if exit != 0 {
				wantStatus = 1
			}
			want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(agentWorkStatus+tc.agent), execution)
			if status != wantStatus || !strings.Contains(output, "workflow output\nlogin input\n") || !reflect.DeepEqual(calls, want) {
				t.Fatalf("args=%v status=%d output=%s calls=%v want=%v", args, status, output, calls, want)
			}
		}
	}
}

// TestAgentWorkTerminalRules proves run allocates a TTY only for two terminal streams while login and session require both through RunWithHost and fake Podman.
func TestAgentWorkTerminalRules(t *testing.T) {
	for _, verb := range []string{"run", "login", "session"} {
		for _, terminal := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
			f := sandboxStateFixture(t)
			f.host.IsInputTerminal = func(io.Reader) bool { return terminal[0] }
			f.host.IsOutputTerminal = func(io.Writer) bool { return terminal[1] }
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[agentWorkStatus+"claude"] = response{Output: `{"name":"claude","enabled":true,"installed_version":"1.2.3","session":true}`}
			execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i")
			interactive := terminal[0] && terminal[1]
			if interactive {
				execution = append(execution, "-t")
			}
			execution = append(execution, shellContainer, integrationManager, "agents", verb, "claude")
			if verb == "login" {
				execution = append(execution, "browser")
			}
			f.responses[strings.Join(execution, " ")] = response{}
			status, output, calls := f.run(t, "agents", verb, "agent01", "claude")
			want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(agentWorkStatus+"claude"))
			wantStatus := 0
			if verb == "run" || interactive {
				want = append(want, execution)
			} else {
				wantStatus = 1
				if !strings.Contains(output, "interactive terminal") {
					t.Errorf("missing terminal diagnostic: %s", output)
				}
			}
			if status != wantStatus || !reflect.DeepEqual(calls, want) {
				t.Fatalf("verb=%s terminal=%v status=%d output=%s calls=%v want=%v", verb, terminal, status, output, calls, want)
			}
		}
	}
}

// TestAgentRunPreservesStreams proves piped input and separate output streams pass through unchanged at the RunWithHost and fake Podman seam.
func TestAgentRunPreservesStreams(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = strings.NewReader("piped input\n")
	f.host.IsInputTerminal = func(io.Reader) bool { return false }
	f.host.IsOutputTerminal = func(io.Writer) bool { return false }
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"codex"] = response{Output: `{"name":"codex","enabled":true,"installed_version":"1.2.3"}`}
	execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents run codex")
	f.responses[strings.Join(execution, " ")] = response{Output: "agent output\n", Stderr: "agent error\n", Exit: 23, EchoStdin: true}
	data, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	var stdout, stderr bytes.Buffer
	status := cli.RunWithHost([]string{"agents", "run", "agent01", "codex"}, &stdout, &stderr, f.host)
	if status != 23 || stdout.String() != "agent output\npiped input\n" || stderr.String() != "agent error\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

// TestAgentSessionUsesSelectedWindowsTarget proves interactive session dispatch and both administrative queries stay on the selected machine through RunWithHost and fake Podman.
func TestAgentSessionUsesSelectedWindowsTarget(t *testing.T) {
	f := windowsHost(t)
	configureSandboxQueries(f, "default", "--connection chosen ")
	f.responses["--connection chosen container exists "+shellContainer] = response{}
	f.host.IsInputTerminal = func(io.Reader) bool { return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	f.responses["--connection chosen "+agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses["--connection chosen "+agentWorkStatus+"codex"] = response{Output: `{"name":"codex","enabled":true,"installed_version":"1.2.3"}`}
	execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i -t " + shellContainer + " " + integrationManager + " agents session codex")
	f.responses["--connection chosen "+strings.Join(execution, " ")] = response{}
	status, output, calls := f.run(t, "agents", "session", "agent01", "codex")
	want := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
	for _, call := range append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(agentWorkStatus+"codex"), execution) {
		want = append(want, append([]string{"--connection", "chosen"}, call...))
	}
	if status != 0 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%s calls=%v want=%v", status, output, calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected host SSH calls: %v", calls)
	}
}
