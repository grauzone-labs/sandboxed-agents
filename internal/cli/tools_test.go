package cli_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

const toolManagerPrefix = "exec --user=1000:1000 --env HOME=/home/agent " + shellContainer + " " + integrationManager + " tools "

// TestToolsParseRejectsInvalidRequestsBeforePodman proves unknown Tool names, invalid channels, invalid or conflicting selectors, and unknown options fail at cli.Parse with the expected diagnostics, before any Podman call.
func TestToolsParseRejectsInvalidRequestsBeforePodman(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tools", "enable", "demo", "t3kode"}, `unknown tool "t3kode": valid names: t3code`},
		{[]string{"tools", "status", "demo", "codex"}, `unknown tool "codex": valid names: t3code`},
		{[]string{"tools", "enable", "demo", "t3code", "--channel", "preview"}, `invalid --channel "preview": valid channels: latest, nightly`},
		{[]string{"tools", "update", "demo", "t3code", "--channel", "stable"}, `invalid --channel "stable": valid channels: latest, nightly`},
		{[]string{"tools", "enable", "demo", "t3code", "--version", "latest"}, `invalid version "latest"`},
		{[]string{"tools", "enable", "demo", "t3code", "--version", "^0.1.2"}, `invalid version "^0.1.2"`},
		{[]string{"tools", "update", "demo", "t3code", "--version", "0.1.2+build.4"}, `invalid pin version "0.1.2+build.4"`},
		{[]string{"tools", "enable", "demo", "t3code", "--channel", "latest", "--version", "0.1.2"}, "--channel and --version conflict"},
		{[]string{"tools", "update", "demo", "t3code", "--version", "0.1.2", "--channel", "nightly"}, "--channel and --version conflict"},
		{[]string{"tools", "status", "demo", "t3code", "--channel", "latest"}, `unknown option "--channel"`},
		{[]string{"tools", "enable", "demo", "t3code", "--force"}, `unknown option "--force"`},
		{[]string{"tools", "update", "demo", "t3code", "--unpin"}, `unknown option "--unpin"`},
		{[]string{"tools", "enable", "demo"}, "wrong number of arguments for tools enable"},
		{[]string{"tools", "delete", "demo", "t3code"}, `unknown command "tools delete"`},
		{[]string{"tools"}, "tools requires a subcommand"},
	} {
		_, err := cli.Parse(tc.args, "default")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args=%q error=%v; want %q", tc.args, err, tc.want)
		}
	}
	request, err := cli.Parse([]string{"tools", "enable", "demo", "t3code", "--version", "0.1.3-nightly.20261023.3600"}, "team")
	if err != nil || request.Command != "tools enable" || request.Name != "demo" || request.Group != "team" || request.Args[1] != "t3code" || request.Options["version"] != "0.1.3-nightly.20261023.3600" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
}

// TestToolsCommandsRunTheManagerAsAgent proves enable and update pass their selectors to the manager started as agent, preserve its output, and normalize operation and transport failures through RunWithHost and fake Podman.
func TestToolsCommandsRunTheManagerAsAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		request string
		answer  response
		status  int
		message string
	}{
		{"enable", []string{"enable", "agent01", "t3code"}, "enable t3code", response{Output: "t3code enabled: version 0.1.2 (channel latest)\n"}, 0, "t3code enabled: version 0.1.2 (channel latest)"},
		{"enable-channel", []string{"enable", "agent01", "t3code", "--channel", "nightly"}, "enable t3code --channel nightly", response{Output: "t3code enabled: version 0.1.3-nightly.1 (channel nightly)\n"}, 0, "channel nightly"},
		{"enable-version", []string{"enable", "agent01", "t3code", "--version=0.1.1"}, "enable t3code --version 0.1.1", response{Output: "t3code enabled: version 0.1.1 (pin 0.1.1)\n"}, 0, "pin 0.1.1"},
		{"update", []string{"update", "agent01", "t3code", "--channel", "latest"}, "update t3code --channel latest", response{Output: "t3code enabled: version 0.1.2 (channel latest)\n"}, 0, "channel latest"},
		{"operation-failure", []string{"enable", "agent01", "t3code", "--version", "9.9.9"}, "enable t3code --version 9.9.9", response{Stderr: "t3code version 9.9.9 is not published: npm error 404\n", Exit: 1}, 1, "t3code version 9.9.9 is not published"},
		{"transport-failure", []string{"enable", "agent01", "t3code"}, "enable t3code", response{Stderr: "transport failed\n", Exit: 125}, 1, "manager does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","pin":"0.1.1"}`}
			f.responses[toolManagerPrefix+tc.request] = tc.answer
			status, output, calls := f.run(t, append([]string{"tools"}, tc.args...)...)
			if status != tc.status || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want status=%d message=%q", status, output, tc.status, tc.message)
			}
			if tc.status != 0 && tc.answer.Exit == 1 && !strings.Contains(output, "tools enable t3code: t3code version 9.9.9 is not published") {
				t.Fatalf("operation failure lost its command: %s", output)
			}
			want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe))
			if tc.args[0] == "update" {
				want = append(want, strings.Fields(toolManagerPrefix+"status t3code"))
			}
			want = append(want, strings.Fields(toolManagerPrefix+tc.request))
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestToolsStatusRendersSelection proves status renders the enabled version with its channel or pin, an absent selection, and a malformed answer as an unavailable manager through RunWithHost and fake Podman.
func TestToolsStatusRendersSelection(t *testing.T) {
	for _, tc := range []struct {
		answer  response
		status  int
		message string
	}{
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","pin":""}`}, 0, "t3code: enabled; version 0.1.2; channel latest; pin none\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","channel":"","pin":"0.1.1"}`}, 0, "t3code: enabled; version 0.1.1; channel none; pin 0.1.1\n"},
		{response{Output: `{"name":"t3code","enabled":false,"installed_version":"","channel":"","pin":""}`}, 0, "t3code: not enabled\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","pin":"0.1.2"}`}, 1, "manager does not answer"},
		{response{Output: "not json"}, 1, "manager does not answer"},
		{response{Exit: 125, Stderr: "transport failed\n"}, 1, "manager does not answer"},
		{response{Exit: 1, Stderr: "invalid tool selection\n"}, 1, "invalid tool selection"},
	} {
		f := sandboxStateFixture(t)
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses[toolManagerPrefix+"status t3code"] = tc.answer
		status, output, calls := f.run(t, "tools", "status", "agent01", "t3code")
		if status != tc.status || !strings.Contains(output, tc.message) || (tc.answer.Exit == 1 && strings.Contains(output, "manager does not answer")) {
			t.Fatalf("answer=%+v status=%d output=%q want %d %q", tc.answer, status, output, tc.status, tc.message)
		}
		want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(toolManagerPrefix+"status t3code"))
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls=%v want=%v", calls, want)
		}
	}
}

// TestToolsChecksPrecedeChanges proves a stopped sandbox, an unanswering manager, and an update of a Tool that is not enabled each refuse before any tools request reaches the manager through RunWithHost and fake Podman.
func TestToolsChecksPrecedeChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(f *fixture)
		args    []string
		message string
	}{
		{"stopped", func(f *fixture) {
			f.responses["container inspect "+shellContainer] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
		}, []string{"tools", "enable", "agent01", "t3code"}, "start agent01"},
		{"manager", func(f *fixture) {
			f.responses[agentWorkProbe] = response{Exit: 125}
		}, []string{"tools", "status", "agent01", "t3code"}, "manager does not answer"},
		{"not-enabled", func(f *fixture) {
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":false}`}
		}, []string{"tools", "update", "agent01", "t3code"}, "t3code not enabled; run tools enable agent01 t3code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			tc.prepare(f)
			status, output, calls := f.run(t, tc.args...)
			if status != 1 || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want %q", status, output, tc.message)
			}
			for _, call := range calls {
				if len(call) > 0 && call[0] == "exec" && call[len(call)-2] != "status" && call[len(call)-1] != "version" {
					t.Fatalf("tools change reached the manager: %v", call)
				}
			}
			if tc.name == "manager" && (!strings.Contains(output, "check agent01") || !strings.Contains(output, "restart agent01")) {
				t.Fatalf("missing recovery commands: %s", output)
			}
		})
	}
}
