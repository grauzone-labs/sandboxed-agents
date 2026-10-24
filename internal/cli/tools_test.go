package cli_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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
		{[]string{"tools", "pairing", "demo", "t3code"}, `unknown command "tools pairing"`},
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
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","pin":"","service_state":"running","service_log":"/home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log"}`}, 0, "t3code: enabled; version 0.1.2; channel latest; pin none; service running; log /home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log; tailnet node absent; run tools tailscale agent01 t3code login\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","channel":"","pin":"0.1.1","service_state":"failed","service_failure":"failed after 5 consecutive failed starts","service_log":"/home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log"}`}, 0, "t3code: enabled; version 0.1.1; channel none; pin 0.1.1; service failed: failed after 5 consecutive failed starts; log /home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log; tailnet node absent; run tools tailscale agent01 t3code login\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","channel":"","pin":"0.1.1","service_state":"stopped","service_log":"/x/service.log"}`}, 0, "t3code: enabled; version 0.1.1; channel none; pin 0.1.1; service stopped; log /x/service.log; tailnet node absent; run tools tailscale agent01 t3code login\n"},
		{response{Output: `{"name":"t3code","enabled":false,"installed_version":"","channel":"","pin":""}`}, 0, "t3code: not enabled\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","pin":"0.1.2"}`}, 1, "manager does not answer"},
		{response{Output: "not json"}, 1, "manager does not answer"},
		{response{Exit: 125, Stderr: "transport failed\n"}, 1, "manager does not answer"},
		{response{Exit: 1, Stderr: "invalid tool selection\n"}, 1, "invalid tool selection"},
	} {
		f := sandboxStateFixture(t)
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses[toolManagerPrefix+"status t3code --connect"] = tc.answer
		status, output, calls := f.run(t, "tools", "status", "agent01", "t3code")
		if status != tc.status || !strings.Contains(output, tc.message) || (tc.answer.Exit == 1 && strings.Contains(output, "manager does not answer")) {
			t.Fatalf("answer=%+v status=%d output=%q want %d %q", tc.answer, status, output, tc.status, tc.message)
		}
		// The user's status asks for the account connection state; the pre-checks of other commands do not.
		want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(toolManagerPrefix+"status t3code --connect"))
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("calls=%v want=%v", calls, want)
		}
	}
}

// TestToolsChecksPrecedeChanges proves a stopped sandbox, an unanswering manager, an update of a Tool that is not enabled, and a status answer that names a manager failure each refuse before any tools change reaches the manager, through RunWithHost and fake Podman.
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
		{"status-connect", func(f *fixture) {
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[toolManagerPrefix+"status t3code --connect"] = response{Exit: 1, Stderr: "invalid tool selection\n"}
		}, []string{"tools", "status", "agent01", "t3code"}, "invalid tool selection"},
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
				joined := strings.Join(call, " ")
				if call[0] == "exec" && !strings.Contains(joined, " tools status ") && call[len(call)-1] != "version" {
					t.Fatalf("tools change reached the manager: %v", call)
				}
			}
			if tc.name == "manager" && (!strings.Contains(output, "check agent01") || !strings.Contains(output, "restart agent01")) {
				t.Fatalf("missing recovery commands: %s", output)
			}
		})
	}
}

// TestLifecycleCommandsRestoreToolServices proves up on an existing sandbox, start, and restart ask the manager, as agent, to restore the tool services once the sandbox runs, pass its output through, and that a service that does not start keeps the sandbox running, names tools status, and exits non-zero through RunWithHost and fake Podman.
func TestLifecycleCommandsRestoreToolServices(t *testing.T) {
	for _, command := range []string{"up", "start", "restart"} {
		for _, outcome := range []string{"running", "failed", "transport"} {
			t.Run(command+"/"+outcome, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.responses["container inspect "+shellContainer] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222","io.sandboxed-agents.toolchains":"none"}},"State":{"Running":false}}]`}
				f.responses["start "+shellContainer] = response{}
				f.responses["restart "+shellContainer] = response{}
				restore := strings.Fields(toolManagerPrefix + "restore")
				switch outcome {
				case "running":
					f.responses[strings.Join(restore, " ")] = response{Output: "t3code service running\n"}
				case "failed":
					f.responses[strings.Join(restore, " ")] = response{Stderr: "t3code service failed to start: failed after 5 consecutive failed starts; log: /home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log\n", Exit: 1}
				case "transport":
					f.responses[strings.Join(restore, " ")] = response{Stderr: "transport failed\n", Exit: 125}
				}
				args := []string{command, "agent01"}
				status, output, calls := f.run(t, args...)
				if outcome == "running" {
					if status != 0 || !strings.Contains(output, "sandbox agent01 is running") || !strings.Contains(output, "t3code service running") {
						t.Fatalf("status=%d output=%s", status, output)
					}
				} else {
					if status != 1 || !strings.Contains(output, "sandbox agent01 is running") || !strings.Contains(output, "sandbox agent01 is running, but") {
						t.Fatalf("status=%d output=%s", status, output)
					}
					if outcome == "failed" && (!strings.Contains(output, "tools status agent01 t3code") || !strings.Contains(output, "consecutive failed starts")) {
						t.Fatalf("failed restore does not name tools status: %s", output)
					}
					if outcome == "transport" && !strings.Contains(output, "manager does not answer") {
						t.Fatalf("transport failure not reported: %s", output)
					}
				}
				if !reflect.DeepEqual(calls[len(calls)-1], restore) {
					t.Fatalf("restore is not the last call: %v", calls)
				}
				// A stopped sandbox is started by every one of the three commands.
				if !reflect.DeepEqual(calls[len(calls)-2], []string{"start", shellContainer}) {
					t.Fatalf("restore does not follow the start: %v", calls)
				}
				for _, call := range calls {
					if call[0] == "stop" || call[0] == "rm" || call[0] == "create" || (call[0] == "exec" && call[len(call)-1] != "restore") {
						t.Fatalf("lifecycle command issued %v", call)
					}
				}
			})
		}
	}
}

// TestStopDoesNotTouchToolServices proves stop neither restores nor asks about tool services, because the container stop ends every process, through RunWithHost and fake Podman.
func TestStopDoesNotTouchToolServices(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses["exec --user=0:0 "+shellContainer+" "+integrationManager+" sessions"] = response{}
	f.responses["stop "+shellContainer] = response{}
	status, output, calls := f.run(t, "stop", "agent01")
	if status != 0 || !strings.Contains(output, "sandbox agent01 is stopped") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		if call[0] == "exec" && call[len(call)-1] == "restore" {
			t.Fatalf("stop restored tool services: %v", calls)
		}
	}
}

// TestToolsCommandsChangeNoPodmanObjects proves tools enable, tools status, tools tailscale login, tools pair, and the tool service restore issue only manager execs, never a create, start, publish, device, capability, mount, or network change, through RunWithHost and fake Podman.
func TestToolsCommandsChangeNoPodmanObjects(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[toolManagerPrefix+"enable t3code"] = response{Output: "t3code enabled: version 0.1.2 (channel latest); service running\n"}
	f.responses[toolManagerPrefix+"status t3code --connect"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
	f.responses[toolManagerPrefix+"restore"] = response{Output: "t3code service running\nt3code tailnet node running\n"}
	f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
	f.responses[toolManagerPrefix+"status t3code --tailnet"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","online":true,"https":"enabled"}}`}
	f.responses["exec --user=1000:1000 --env HOME=/home/agent -i -t "+shellContainer+" "+integrationManager+" tools tailscale t3code login --hostname agent01"] = response{Output: "t3code tailnet node signed in as agent01.tail1234.ts.net; origin https://agent01.tail1234.ts.net/\n"}
	f.responses["exec --user=1000:1000 --env HOME=/home/agent -i -t "+shellContainer+" "+integrationManager+" tools pair t3code"] = response{Output: "Pairing URL: https://agent01.tail1234.ts.net/pair#token=TOKEN\n"}
	f.host.IsInputTerminal = func(io.Reader) bool { return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	var all [][]string
	for _, args := range [][]string{{"tools", "enable", "agent01", "t3code"}, {"tools", "status", "agent01", "t3code"}, {"tools", "tailscale", "agent01", "t3code", "login"}, {"tools", "pair", "agent01", "t3code"}, {"start", "agent01"}} {
		status, output, calls := f.run(t, args...)
		if status != 0 {
			t.Fatalf("args=%v status=%d output=%s", args, status, output)
		}
		all = append(all, calls...)
	}
	noMutation(t, all)
	for _, call := range all {
		joined := strings.Join(call, " ")
		for _, forbidden := range []string{"--publish", "--device", "--cap-add", "--mount", "--network", "create", "update"} {
			if call[0] == forbidden || strings.Contains(joined, " "+forbidden+" ") || strings.Contains(joined, " "+forbidden+"=") {
				t.Fatalf("tool work changed Podman objects: %v", call)
			}
		}
		if call[0] == "exec" && call[1] != "--user=1000:1000" && call[1] != "--user=0:0" {
			t.Fatalf("exec without an explicit identity: %v", call)
		}
	}
}

// TestCheckReportsToolServices proves check NAME lists every enabled Tool with its service state through the manager's root query, counts a failed service as a problem, and stays read-only through RunWithHost and fake Podman.
func TestCheckReportsToolServices(t *testing.T) {
	for _, tc := range []struct {
		answer  response
		status  int
		message string
	}{
		{response{Output: `[{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log"}]` + "\n"}, 0, "tool t3code: version 0.1.2; channel latest; service running; log /x/service.log"},
		{response{Output: `[{"name":"t3code","enabled":true,"installed_version":"0.1.1","pin":"0.1.1","service_state":"failed","service_failure":"failed after 5 consecutive failed starts","service_log":"/x/service.log"}]` + "\n"}, 1, "tool t3code: service failed: failed after 5 consecutive failed starts; log /x/service.log"},
		{response{Output: "[]\n"}, 0, "manager: reachable"},
		{response{Exit: 2, Stderr: "usage: sandboxed-agents-manager version | sessions | ssh-host-key\n"}, 0, "manager: reachable"},
		{response{Output: "not json"}, 1, "invalid tool list answer"},
		{response{Exit: 1, Stderr: "cannot read tool selection\n"}, 1, "tool services cannot be read"},
		{response{Exit: 125, Stderr: "transport failed\n"}, 1, "manager does not answer the tool service query"},
	} {
		f := sandboxStateFixture(t)
		f.responses["container inspect "+shellContainer] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}},"HostConfig":{"Memory":8589934592,"MemorySwap":8589934592,"NanoCpus":4000000000,"CpuQuota":400000,"CpuPeriod":100000,"PidsLimit":2048,"ShmSize":1073741824},"State":{"Running":true}}]`}
		for _, volume := range sandboxVolumes {
			f.responses["volume exists "+volume] = response{}
		}
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses["exec --user=0:0 "+shellContainer+" "+integrationManager+" tools list"] = tc.answer
		status, output, calls := f.run(t, "check", "agent01")
		if status != tc.status || !strings.Contains(output, tc.message) {
			t.Fatalf("answer=%+v status=%d output=%s want %d %q", tc.answer, status, output, tc.status, tc.message)
		}
		noMutation(t, calls)
		for _, call := range calls {
			if call[0] == "exec" && call[len(call)-1] != "version" && call[len(call)-1] != "list" {
				t.Fatalf("check changed the sandbox: %v", call)
			}
		}
	}
}

// TestSessionGuardStillAppliesWithToolServices proves stop and restart refuse running agent sessions without --force while tool services exist, and restore nothing, through RunWithHost and fake Podman.
func TestSessionGuardStillAppliesWithToolServices(t *testing.T) {
	for _, command := range []string{"stop", "restart"} {
		f := sandboxStateFixture(t)
		f.responses["exec --user=0:0 "+shellContainer+" "+integrationManager+" sessions"] = response{Output: "codex-main\n"}
		f.responses[toolManagerPrefix+"restore"] = response{Output: "t3code service running\n"}
		status, output, calls := f.run(t, command, "agent01")
		if status != 1 || !strings.Contains(output, "running agent sessions: codex-main; use --force") {
			t.Fatalf("%s status=%d output=%s", command, status, output)
		}
		for _, call := range calls {
			if call[0] == "stop" || call[0] == "restart" || (call[0] == "exec" && call[len(call)-1] == "restore") {
				t.Fatalf("%s acted despite the session guard: %v", command, call)
			}
		}
	}
}

// TestInterruptedUpdateRecoveryRestoresToolServices proves the recovery of an interrupted update restores the tool services of a running sandbox after the backup is removed, and that a service that does not start keeps the recovered sandbox running and exits non-zero naming tools status, through the executable CLI seam and fake Podman.
func TestInterruptedUpdateRecoveryRestoresToolServices(t *testing.T) {
	for _, failing := range []bool{false, true} {
		f := cleanupFixture(t, true, true)
		restore := toolManagerPrefix + "restore"
		restore = strings.Replace(restore, shellContainer, "sandboxed-agents.default.demo", 1)
		if failing {
			f.responses[restore] = response{Stderr: "t3code service failed to start: failed after 5 consecutive failed starts\n", Exit: 1}
		} else {
			f.responses[restore] = response{Output: "t3code service running\n"}
		}
		status, output, calls := runUpExecutable(t, f, "update", "demo")
		if failing != (status != 0) || !strings.Contains(output, "interrupted update cleaned up") {
			t.Fatalf("failing=%t status=%d output=%s", failing, status, output)
		}
		if failing && (!strings.Contains(output, "tools status demo t3code") || !strings.Contains(output, "consecutive failed starts")) {
			t.Fatalf("failed restore not reported: %s", output)
		}
		if !failing && !strings.Contains(output, "t3code service running") {
			t.Fatalf("restore output missing: %s", output)
		}
		if last := calls[len(calls)-1]; last[len(last)-1] != "restore" {
			t.Fatalf("restore is not the last call: %v", calls)
		}
		for _, call := range calls {
			if call[0] == "stop" {
				t.Fatalf("recovered sandbox was stopped: %v", call)
			}
		}
	}
}

// TestUpdateReportsFailedToolServiceRestore proves update NAME restores the tool services after a successful replacement and after a rollback that restarted the old sandbox, and that a service that does not start leaves the sandbox running, names tools status, and exits non-zero, through the executable CLI seam and fake processes.
func TestUpdateReportsFailedToolServiceRestore(t *testing.T) {
	for _, stage := range []string{"replacement", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			f := replacementFixture(t)
			restore := strings.Replace(toolManagerPrefix+"restore", shellContainer, "sandboxed-agents.default.demo", 1)
			f.responses[restore] = response{Stderr: "t3code service failed to start: failed after 5 consecutive failed starts\n", Exit: 1}
			var status int
			var output string
			var calls [][]string
			if stage == "rollback" {
				f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
				f.responses["rm --force sandboxed-agents.default.demo"] = response{}
				f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "start failed", FailOnce: true}
				status, output, calls = runUpExecutableWithShortReadiness(t, f, "update", "demo")
				if !strings.Contains(output, "restored sandbox demo to its previous state") {
					t.Fatalf("rollback missing: %s", output)
				}
			} else {
				status, output, calls = runUpExecutable(t, f, "update", "demo")
				if !strings.Contains(output, "sandbox demo updated") {
					t.Fatalf("update missing: %s", output)
				}
			}
			if status != 1 || !strings.Contains(output, "tools status demo t3code") || !strings.Contains(output, "consecutive failed starts") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if last := calls[len(calls)-1]; last[len(last)-1] != "restore" {
				t.Fatalf("restore is not the last call: %v", calls)
			}
			// The sandbox that the restore found running stays running: nothing stops it after its last start.
			started := -1
			for index, call := range calls {
				if strings.Join(call, " ") == "start sandboxed-agents.default.demo" {
					started = index
				}
			}
			if started < 0 {
				t.Fatalf("sandbox never started: %v", calls)
			}
			for _, call := range calls[started:] {
				if call[0] == "stop" {
					t.Fatalf("sandbox stopped after its last start: %v", calls)
				}
			}
		})
	}
}

// TestUpdateAllCountsUpdatedSandboxesWithFailedToolServices proves update --all counts a replaced sandbox whose tool service did not start as updated, reports the service separately, continues with the others, and exits non-zero, through the executable CLI seam and fake processes.
func TestUpdateAllCountsUpdatedSandboxesWithFailedToolServices(t *testing.T) {
	f := batchUpdateFixture(t, "demo", "next")
	restore := strings.Replace(toolManagerPrefix+"restore", shellContainer, "sandboxed-agents.default.demo", 1)
	f.responses[restore] = response{Stderr: "t3code service failed to start: failed after 5 consecutive failed starts\n", Exit: 1}
	status, output, _ := runUpExecutable(t, f, "update", "--all")
	if status != 1 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, want := range []string{"sandbox demo updated, but", "tools status demo t3code", "sandbox next updated", "2 updated, 0 current, 0 volumes only, 0 not updated", "1 updated sandbox(es) with a tool service that did not start", "1 with a tool service that did not start"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q: %s", want, output)
		}
	}
}

// TestToolsConnectParseRejectsUnknownActions proves tools connect accepts exactly the catalog's connect actions and rejects unknown actions, missing actions, and options at cli.Parse, before any Podman call.
func TestToolsConnectParseRejectsUnknownActions(t *testing.T) {
	for _, action := range []string{"login", "link", "unlink", "logout"} {
		request, err := cli.Parse([]string{"tools", "connect", "demo", "t3code", action}, "default")
		if err != nil || request.Command != "tools connect" || request.Args[2] != action {
			t.Fatalf("action %s: request=%+v error=%v", action, request, err)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tools", "connect", "demo", "t3code", "pair"}, `unknown connect action "pair": valid names: link, login, logout, unlink`},
		{[]string{"tools", "connect", "demo", "t3code"}, "wrong number of arguments for tools connect"},
		{[]string{"tools", "connect", "demo", "t3code", "login", "extra"}, "wrong number of arguments for tools connect"},
		{[]string{"tools", "connect", "demo", "t3code", "login", "--headless"}, `unknown option "--headless"`},
		{[]string{"tools", "connect", "demo", "codex", "login"}, `unknown tool "codex": valid names: t3code`},
	} {
		if _, err := cli.Parse(tc.args, "default"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args=%q error=%v; want %q", tc.args, err, tc.want)
		}
	}
}

// TestToolsConnectRunsTheManagerAsAgentWithTerminalRules proves login and link need an interactive terminal and fail before any manager change without one, unlink and logout run without a terminal, every action runs the manager as agent with the caller's input, and an upstream failure exits non-zero naming the action, through RunWithHost and fake Podman.
func TestToolsConnectRunsTheManagerAsAgentWithTerminalRules(t *testing.T) {
	for _, action := range []string{"login", "link", "unlink", "logout"} {
		for _, terminal := range [][2]bool{{false, false}, {true, false}, {true, true}} {
			for _, exit := range []int{0, 1} {
				f := sandboxStateFixture(t)
				f.host.IsInputTerminal = func(io.Reader) bool { return terminal[0] }
				f.host.IsOutputTerminal = func(io.Writer) bool { return terminal[1] }
				f.host.Stdin = strings.NewReader("connect input\n")
				f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
				f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
				execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i")
				interactive := terminal[0] && terminal[1]
				if interactive {
					execution = append(execution, "-t")
				}
				execution = append(execution, shellContainer, integrationManager, "tools", "connect", "t3code", action)
				f.responses[strings.Join(execution, " ")] = response{Output: "upstream output\n", Exit: exit, EchoStdin: true}
				status, output, calls := f.run(t, "tools", "connect", "agent01", "t3code", action)
				needsTerminal := action == "login" || action == "link"
				want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(toolManagerPrefix+"status t3code"))
				switch {
				case needsTerminal && !interactive:
					if status != 1 || !strings.Contains(output, "tools connect t3code "+action+" needs an interactive terminal") || strings.Contains(output, "upstream output") {
						t.Fatalf("%s terminal=%v status=%d output=%s", action, terminal, status, output)
					}
				default:
					want = append(want, execution)
					if (exit == 0) != (status == 0) || !strings.Contains(output, "upstream output\nconnect input\n") {
						t.Fatalf("%s terminal=%v exit=%d status=%d output=%s", action, terminal, exit, status, output)
					}
					if exit != 0 && !strings.Contains(output, "tools connect t3code "+action+" failed") {
						t.Fatalf("%s failure not named: %s", action, output)
					}
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("%s terminal=%v calls=%v want=%v", action, terminal, calls, want)
				}
			}
		}
	}
}

// TestToolsConnectChecksPrecedeTheAction proves a stopped sandbox, an unanswering manager, a Tool that is not enabled, and a transport failure each refuse or report before or instead of the connect action, through RunWithHost and fake Podman.
func TestToolsConnectChecksPrecedeTheAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(f *fixture)
		message string
	}{
		{"stopped", func(f *fixture) {
			f.responses["container inspect "+shellContainer] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
		}, "start agent01"},
		{"manager", func(f *fixture) { f.responses[agentWorkProbe] = response{Exit: 125} }, "manager does not answer"},
		{"not-enabled", func(f *fixture) {
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":false}`}
		}, "t3code not enabled; run tools enable agent01 t3code"},
		{"transport", func(f *fixture) {
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
			f.responses["exec --user=1000:1000 --env HOME=/home/agent -i "+shellContainer+" "+integrationManager+" tools connect t3code logout"] = response{Exit: 125}
		}, "manager does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.host.IsInputTerminal = func(io.Reader) bool { return false }
			f.host.IsOutputTerminal = func(io.Writer) bool { return false }
			tc.prepare(f)
			status, output, calls := f.run(t, "tools", "connect", "agent01", "t3code", "logout")
			if status != 1 || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want %q", status, output, tc.message)
			}
			if tc.name != "transport" {
				for _, call := range calls {
					if call[0] == "exec" && call[len(call)-1] == "logout" {
						t.Fatalf("connect action reached the manager: %v", call)
					}
				}
			}
		})
	}
}

// TestToolsStatusRendersTheConnectState proves status renders the account, linked, relay, and reachability state, a missing login, and a connect status that could not be read, through RunWithHost and fake Podman.
func TestToolsStatusRendersTheConnectState(t *testing.T) {
	for _, tc := range []struct {
		connect string
		want    string
	}{
		{`{"authenticated":false,"linked":false,"relay_client":"missing"}`, "service running; log /x/service.log; T3 Connect not signed in, not linked; relay client missing; tailnet node absent"},
		{`{"authenticated":true,"linked":true,"account":"user_123","relay_client":"available","reachability":"{\"status\":\"unreachable\",\"reason\":\"cannot reach port 7844\"}"}`, "T3 Connect signed in as user_123, linked; relay client available; relay failure: {\"status\":\"unreachable\",\"reason\":\"cannot reach port 7844\"}; tailnet"},
		{`{"authenticated":true,"linked":true,"account":"user_123","relay_client":"available","reachability":"{\"status\":\"reachable\"}","reachable":true}`, "T3 Connect signed in as user_123, linked; relay client available; relay reachable; tailnet"},
		{`{"error":"status unavailable: status failed"}`, "T3 Connect status unavailable: status failed; tailnet node absent; run tools tailscale agent01 t3code login\n"},
	} {
		f := sandboxStateFixture(t)
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses[toolManagerPrefix+"status t3code --connect"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log","connect":` + tc.connect + `}`}
		status, output, _ := f.run(t, "tools", "status", "agent01", "t3code")
		if status != 0 || !strings.Contains(output, tc.want) {
			t.Fatalf("connect=%s status=%d output=%q want %q", tc.connect, status, output, tc.want)
		}
	}
}

// TestToolsDeleteRunsTheManagerAsAgent proves delete runs the manager as agent without a terminal, passes its output through, needs no enabled Tool beforehand, exits non-zero with the manager's diagnostic on a failure, and refuses on a stopped sandbox or an unanswering manager, through RunWithHost and fake Podman.
func TestToolsDeleteRunsTheManagerAsAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(f *fixture)
		status  int
		message string
	}{
		{"deleted", func(f *fixture) {
			f.responses[toolManagerPrefix+"delete t3code"] = response{Output: "moved the worktree /home/agent/.local/share/sandboxed-agents/tools/t3code/home/worktrees/repo/branch to /home/agent/t3code-files/worktrees/repo/branch\nt3code deleted: the service, the installation, the user data, the credentials, and the selection are removed\n"}
		}, 0, "t3code deleted"},
		{"nothing", func(f *fixture) {
			f.responses[toolManagerPrefix+"delete t3code"] = response{Output: "t3code not enabled; nothing to delete\n"}
		}, 0, "nothing to delete"},
		{"refused", func(f *fixture) {
			f.responses[toolManagerPrefix+"delete t3code"] = response{Stderr: "t3code not deleted; a T3 Connect login is in progress; cancel it or wait, then retry\n", Exit: 1}
		}, 1, "tools delete t3code: t3code not deleted; a T3 Connect login is in progress"},
		{"stopped", func(f *fixture) {
			f.responses["container inspect "+shellContainer] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
		}, 1, "start agent01"},
		{"manager", func(f *fixture) { f.responses[agentWorkProbe] = response{Exit: 125} }, 1, "manager does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			tc.prepare(f)
			status, output, calls := f.run(t, "tools", "delete", "agent01", "t3code")
			if status != tc.status || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want %d %q", status, output, tc.status, tc.message)
			}
			for _, call := range calls {
				if call[0] == "exec" && call[len(call)-1] == "t3code" && call[len(call)-2] == "delete" && (tc.name == "stopped" || tc.name == "manager") {
					t.Fatalf("delete reached the manager: %v", call)
				}
				if call[0] == "exec" && strings.Contains(strings.Join(call, " "), " tools status ") {
					t.Fatalf("delete asked for the status first: %v", call)
				}
			}
			if tc.status == 0 && !reflect.DeepEqual(calls[len(calls)-1], strings.Fields(toolManagerPrefix+"delete t3code")) {
				t.Fatalf("delete is not the last call: %v", calls)
			}
		})
	}
	if _, err := cli.Parse([]string{"tools", "delete", "demo", "t3code", "--force"}, "default"); err == nil || !strings.Contains(err.Error(), `unknown option "--force"`) {
		t.Fatalf("delete accepted --force: %v", err)
	}
}

// TestToolsPairAndTailscaleParseRejectsInvalidRequestsBeforePodman proves tools pair accepts --ttl or --revoke but not both, rejects a malformed validity, tools tailscale accepts exactly login and logout, and both reject unknown options and argument counts at cli.Parse, before any Podman call.
func TestToolsPairAndTailscaleParseRejectsInvalidRequestsBeforePodman(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"tools", "pair", "demo", "t3code", "--ttl", "10m", "--revoke"}, "--ttl and --revoke conflict"},
		{[]string{"tools", "pair", "demo", "t3code", "--ttl", "10"}, `invalid --ttl "10": use a number with ms, s, m, h, d, or w`},
		{[]string{"tools", "pair", "demo", "t3code", "--ttl", "15 minutes"}, `invalid --ttl "15 minutes"`},
		{[]string{"tools", "pair", "demo", "t3code", "--tailscale"}, `unknown option "--tailscale"`},
		{[]string{"tools", "pair", "demo", "t3code", "extra"}, "wrong number of arguments for tools pair"},
		{[]string{"tools", "pair", "demo", "codex"}, `unknown tool "codex": valid names: t3code`},
		{[]string{"tools", "tailscale", "demo", "t3code", "up"}, `unknown tailscale action "up": valid names: login, logout`},
		{[]string{"tools", "tailscale", "demo", "t3code"}, "wrong number of arguments for tools tailscale"},
		{[]string{"tools", "tailscale", "demo", "t3code", "login", "--hostname", "x"}, `unknown option "--hostname"`},
		{[]string{"tools", "connect", "demo", "t3code", "pair"}, `unknown connect action "pair"`},
	} {
		if _, err := cli.Parse(tc.args, "default"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("args=%q error=%v; want %q", tc.args, err, tc.want)
		}
	}
	request, err := cli.Parse([]string{"tools", "pair", "demo", "t3code", "--ttl", "10m"}, "default")
	if err != nil || request.Command != "tools pair" || request.Options["ttl"] != "10m" || request.Options["revoke"] != "" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	request, err = cli.Parse([]string{"tools", "pair", "demo", "t3code", "--revoke"}, "default")
	if err != nil || request.Options["revoke"] != "true" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	for _, action := range []string{"login", "logout"} {
		request, err := cli.Parse([]string{"tools", "tailscale", "demo", "t3code", action}, "default")
		if err != nil || request.Command != "tools tailscale" || request.Args[2] != action {
			t.Fatalf("action %s: request=%+v error=%v", action, request, err)
		}
	}
}

// TestToolsPairRunsTheManagerAsAgentWithTerminalRules proves pair needs an enabled Tool, a running service, and a signed-in, named, online tailnet node on a tailnet with HTTPS certificates whose Serve mapping fronts the service, each refused with its own message from the status the host read before the terminal check, needs an interactive terminal, runs the manager as agent with the caller's terminal and the validity, names an upstream failure, and that --revoke runs without a terminal and without the service, through RunWithHost and fake Podman.
func TestToolsPairRunsTheManagerAsAgentWithTerminalRules(t *testing.T) {
	tailnet := func(extra string) response {
		return response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","online":true,"https":"enabled"` + extra + `}}`}
	}
	running := tailnet(``)
	for _, tc := range []struct {
		name     string
		args     []string
		status   response
		terminal bool
		exit     int
		want     string
		exec     string
	}{
		{"not-enabled", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":false}`}, true, 1, "t3code not enabled; run tools enable agent01 t3code", ""},
		{"service-stopped", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"stopped","service_failure":"service exited after 2s: exit status 1"}`}, false, 1, "t3code service is stopped; run tools enable agent01 t3code to start it, or tools status agent01 t3code for its failure", ""},
		{"no-node", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}, false, 1, "t3code tailnet node not signed in; run tools tailscale agent01 t3code login", ""},
		{"node-stopped", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"stopped"}}`}, false, 1, "t3code tailnet node is stopped; run tools enable agent01 t3code to start it, or tools status agent01 t3code for its failure", ""},
		{"signed-out", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"NeedsLogin","https":"unknown"}}`}, false, 1, "t3code tailnet node not signed in (NeedsLogin); run tools tailscale agent01 t3code login", ""},
		{"status-unavailable", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","error":"status unavailable: failed to connect to local tailscaled"}}`}, false, 1, "t3code tailnet node status unavailable: failed to connect to local tailscaled", ""},
		{"no-magicdns", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"Running","online":true,"https":"enabled"}}`}, false, 1, "the tailnet has no MagicDNS name; enable MagicDNS at https://login.tailscale.com/admin/dns, then run tools pair agent01 t3code again", ""},
		{"no-https", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","online":true,"https":"disabled"}}`}, false, 1, "HTTPS certificates are not enabled on the tailnet; enable them at https://login.tailscale.com/admin/dns, then run tools pair agent01 t3code again", ""},
		{"offline", []string{"tools", "pair", "agent01", "t3code"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","https":"enabled"}}`}, false, 1, "t3code tailnet node is offline; check the sandbox's outbound connectivity and tools status agent01 t3code", ""},
		{"foreign-serve", []string{"tools", "pair", "agent01", "t3code"}, tailnet(`,"serve":"http://127.0.0.1:9999"`), false, 1, "the tailnet node's HTTPS port 443 already fronts http://127.0.0.1:9999, not this service; run tools tailscale agent01 t3code logout and then login to reset the node", ""},
		{"no-terminal", []string{"tools", "pair", "agent01", "t3code"}, running, false, 1, "tools pair t3code needs an interactive terminal", ""},
		{"paired", []string{"tools", "pair", "agent01", "t3code", "--ttl", "10m"}, running, true, 0, "upstream output", "exec --user=1000:1000 --env HOME=/home/agent -i -t " + shellContainer + " " + integrationManager + " tools pair t3code --ttl 10m"},
		{"failed", []string{"tools", "pair", "agent01", "t3code"}, running, true, 1, "tools pair t3code failed", "exec --user=1000:1000 --env HOME=/home/agent -i -t " + shellContainer + " " + integrationManager + " tools pair t3code"},
		{"revoke", []string{"tools", "pair", "agent01", "t3code", "--revoke"}, response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"stopped"}`}, false, 0, "upstream output", "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " tools pair t3code --revoke"},
		{"revoke-failed", []string{"tools", "pair", "agent01", "t3code", "--revoke"}, running, false, 1, "tools pair t3code --revoke failed", "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " tools pair t3code --revoke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.host.IsInputTerminal = func(io.Reader) bool { return tc.terminal }
			f.host.IsOutputTerminal = func(io.Writer) bool { return tc.terminal }
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			// A pairing reads the node's sign-in and Serve state for its step-8 checks; a revocation reads the records alone.
			statusCall := toolManagerPrefix + "status t3code --tailnet"
			if slices.Contains(tc.args, "--revoke") {
				statusCall = toolManagerPrefix + "status t3code"
			}
			f.responses[statusCall] = tc.status
			if tc.exec != "" {
				f.responses[tc.exec] = response{Output: "upstream output\n", Exit: tc.exit}
			}
			status, output, calls := f.run(t, tc.args...)
			if status != tc.exit || !strings.Contains(output, tc.want) {
				t.Fatalf("status=%d output=%s want %d %q", status, output, tc.exit, tc.want)
			}
			want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(statusCall))
			if tc.exec != "" {
				want = append(want, strings.Fields(tc.exec))
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestToolsTailscaleRunsTheManagerAsAgentWithTerminalRules proves login needs an enabled Tool and an interactive terminal and passes the sandbox name as the node's hostname, lowercased with other characters replaced by hyphens, that logout runs without a terminal, and that an upstream failure is named, through RunWithHost and fake Podman.
func TestToolsTailscaleRunsTheManagerAsAgentWithTerminalRules(t *testing.T) {
	enabled := response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
	for _, tc := range []struct {
		name     string
		sandbox  string
		action   string
		status   response
		terminal bool
		exit     int
		want     string
		exec     string
	}{
		{"not-enabled", "agent01", "login", response{Output: `{"name":"t3code","enabled":false}`}, true, 1, "t3code not enabled; run tools enable agent01 t3code", ""},
		{"no-terminal", "agent01", "login", enabled, false, 1, "tools tailscale t3code login needs an interactive terminal", ""},
		{"login", "agent01", "login", enabled, true, 0, "upstream output", "tools tailscale t3code login --hostname agent01"},
		{"login-normalized", "My_Box.1", "login", enabled, true, 0, "upstream output", "tools tailscale t3code login --hostname my-box-1"},
		{"login-failed", "agent01", "login", enabled, true, 1, "tools tailscale t3code login failed", "tools tailscale t3code login --hostname agent01"},
		{"logout", "agent01", "logout", enabled, false, 0, "upstream output", "tools tailscale t3code logout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			container := "sandboxed-agents.default." + tc.sandbox
			f.host.IsInputTerminal = func(io.Reader) bool { return tc.terminal }
			f.host.IsOutputTerminal = func(io.Writer) bool { return tc.terminal }
			for key, value := range f.responses {
				if strings.Contains(key, "agent01") {
					f.responses[strings.ReplaceAll(key, "agent01", tc.sandbox)] = value
				}
			}
			f.responses["exec --user=0:0 "+container+" "+integrationManager+" version"] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses["exec --user=1000:1000 --env HOME=/home/agent "+container+" "+integrationManager+" tools status t3code"] = tc.status
			execution := "exec --user=1000:1000 --env HOME=/home/agent -i "
			if tc.terminal {
				execution += "-t "
			}
			execution += container + " " + integrationManager + " " + tc.exec
			if tc.exec != "" {
				f.responses[execution] = response{Output: "upstream output\n", Exit: tc.exit}
			}
			status, output, calls := f.run(t, "tools", "tailscale", tc.sandbox, "t3code", tc.action)
			if status != tc.exit || !strings.Contains(output, tc.want) {
				t.Fatalf("status=%d output=%s want %d %q", status, output, tc.exit, tc.want)
			}
			last := calls[len(calls)-1]
			if tc.exec != "" && !reflect.DeepEqual(last, strings.Fields(execution)) {
				t.Fatalf("last call=%v want %v", last, strings.Fields(execution))
			}
			if tc.exec == "" && strings.Contains(strings.Join(last, " "), " tools tailscale ") {
				t.Fatalf("action reached the manager: %v", last)
			}
		})
	}
}

// TestToolsStatusRendersTheTailnetAndDevices proves status renders an absent, stopped, signed-out, and signed-in tailnet node with its origin and Serve mapping, a status that could not be read, and the paired devices, through RunWithHost and fake Podman.
func TestToolsStatusRendersTheTailnetAndDevices(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  string
	}{
		{``, "; T3 Connect not signed in, not linked; tailnet node absent; run tools tailscale agent01 t3code login\n"},
		{`,"tailnet":{"node":"stopped","node_failure":"service exited after 2s: exit status 1","node_log":"/x/tailscale/service/service.log"}`, "; tailnet node stopped: service exited after 2s: exit status 1; node log /x/tailscale/service/service.log\n"},
		{`,"tailnet":{"node":"running","backend":"NeedsLogin","https":"unknown"}`, "; tailnet node running, not signed in (NeedsLogin); run tools tailscale agent01 t3code login\n"},
		{`,"tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","online":true,"https":"enabled"}`, "; tailnet node running, signed in as agent01.tail1234.ts.net, origin https://agent01.tail1234.ts.net/; no Serve mapping yet; run tools pair agent01 t3code\n"},
		{`,"tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","serve":"http://127.0.0.1:3773","serves_service":true,"online":true,"https":"enabled"},"devices":{"items":[{"client":"Pixel 8","connected":true,"last_connected":"2026-10-23T12:02:00.000Z"},{"client":"tablet (iPadOS)"}]}`, "; tailnet node running, signed in as agent01.tail1234.ts.net, origin https://agent01.tail1234.ts.net/; Serve maps the origin to the service; paired devices: Pixel 8 (connected), tablet (iPadOS)\n"},
		{`,"tailnet":{"node":"running","backend":"Running","name":"agent01.tail1234.ts.net","origin":"https://agent01.tail1234.ts.net/","serve":"http://127.0.0.1:9999","online":false,"https":"disabled"},"devices":{}`, "; tailnet node running, signed in as agent01.tail1234.ts.net, origin https://agent01.tail1234.ts.net/; Serve fronts http://127.0.0.1:9999, not the service; HTTPS certificates disabled on the tailnet; node offline; no paired device\n"},
		{`,"tailnet":{"node":"running","error":"status unavailable: failed to connect to local tailscaled"},"devices":{"error":"devices unavailable: database locked"}`, "; tailnet node running; tailnet status unavailable: failed to connect to local tailscaled; paired devices unavailable: database locked\n"},
	} {
		f := sandboxStateFixture(t)
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses[toolManagerPrefix+"status t3code --connect"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log","connect":{"authenticated":false,"linked":false}` + tc.extra + `}`}
		status, output, _ := f.run(t, "tools", "status", "agent01", "t3code")
		if status != 0 || !strings.Contains(output, tc.want) {
			t.Fatalf("extra=%s status=%d output=%q want %q", tc.extra, status, output, tc.want)
		}
	}
}

// TestCheckReportsTailnetNodes proves check NAME names the tailnet node's state on the tool line and counts a failed node as a problem through RunWithHost and fake Podman.
func TestCheckReportsTailnetNodes(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		status  int
		message string
	}{
		{`[{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log","tailnet":{"node":"running","node_log":"/x/tailscale/service/service.log"}}]`, 0, "tool t3code: version 0.1.2; channel latest; service running; log /x/service.log; tailnet node running"},
		{`[{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log","tailnet":{"node":"failed","node_failure":"failed after 5 consecutive failed starts","node_log":"/x/tailscale/service/service.log"}}]`, 1, "tool t3code: tailnet node failed: failed after 5 consecutive failed starts; log /x/tailscale/service/service.log"},
	} {
		f := sandboxStateFixture(t)
		f.responses["container inspect "+shellContainer] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}},"HostConfig":{"Memory":8589934592,"MemorySwap":8589934592,"NanoCpus":4000000000,"CpuQuota":400000,"CpuPeriod":100000,"PidsLimit":2048,"ShmSize":1073741824},"State":{"Running":true}}]`}
		for _, volume := range sandboxVolumes {
			f.responses["volume exists "+volume] = response{}
		}
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses["exec --user=0:0 "+shellContainer+" "+integrationManager+" tools list"] = response{Output: tc.answer + "\n"}
		status, output, calls := f.run(t, "check", "agent01")
		if status != tc.status || !strings.Contains(output, tc.message) {
			t.Fatalf("status=%d output=%s want %d %q", status, output, tc.status, tc.message)
		}
		noMutation(t, calls)
	}
}

// TestRestartSucceedsAgainstAManagerWithoutTools proves restart of a sandbox whose manager predates the Tool
// commands does not fail for the missing restore: the manager's usage text with status 2 means the sandbox has
// no Tool, through RunWithHost and fake Podman.
func TestRestartSucceedsAgainstAManagerWithoutTools(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses["container inspect "+shellContainer] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222","io.sandboxed-agents.toolchains":"none"}},"State":{"Running":true}}]`}
	f.responses["restart "+shellContainer] = response{}
	f.responses[integrationProbe] = response{}
	f.responses[strings.Join(strings.Fields(toolManagerPrefix+"restore"), " ")] = response{Exit: 2, Stderr: "usage: sandboxed-agents-manager version | sessions | ssh-host-key\n"}
	status, output, _ := f.run(t, "restart", "agent01")
	if status != 0 || strings.Contains(output, "could not be restored") {
		t.Fatalf("status=%d output=%s", status, output)
	}
}

// TestToolsStatusWaitsLongerForUpstreamQueries proves the status query that runs upstream's status commands gets the longer upstream limit, so an answer slower than the records limit still renders, and that a query that exceeds the upstream limit names the slow upstream queries instead of an unanswering manager, through RunWithHost, fake Podman, and the status limit hook.
func TestToolsStatusWaitsLongerForUpstreamQueries(t *testing.T) {
	restore := cli.SetToolStatusTimeouts(200*time.Millisecond, 3*time.Second)
	defer restore()
	answer := `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running","service_log":"/x/service.log","connect":{"authenticated":false,"linked":false}}`

	slow := sandboxStateFixture(t)
	release := filepath.Join(t.TempDir(), "release")
	slow.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	slow.responses[toolManagerPrefix+"status t3code --connect"] = response{Output: answer, WaitFor: release}
	go func() {
		time.Sleep(600 * time.Millisecond)
		os.WriteFile(release, nil, 0600)
	}()
	status, output, _ := slow.run(t, "tools", "status", "agent01", "t3code")
	if status != 0 || !strings.Contains(output, "t3code: enabled; version 0.1.2") {
		t.Fatalf("slow upstream status: status=%d output=%s", status, output)
	}

	restoreShort := cli.SetToolStatusTimeouts(200*time.Millisecond, 400*time.Millisecond)
	defer restoreShort()
	hung := sandboxStateFixture(t)
	hung.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	hung.responses[toolManagerPrefix+"status t3code --connect"] = response{Output: answer, WaitFor: filepath.Join(t.TempDir(), "never-created")}
	status, output, _ = hung.run(t, "tools", "status", "agent01", "t3code")
	if status != 1 || !strings.Contains(output, "tools status t3code: upstream's status queries did not answer within 0.4s") || strings.Contains(output, "manager does not answer") {
		t.Fatalf("hung upstream status: status=%d output=%s", status, output)
	}
}
