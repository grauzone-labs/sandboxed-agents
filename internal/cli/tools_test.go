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
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","pin":"","service_state":"running","service_log":"/home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log"}`}, 0, "t3code: enabled; version 0.1.2; channel latest; pin none; service running; log /home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","channel":"","pin":"0.1.1","service_state":"failed","service_failure":"failed after 5 consecutive failed starts","service_log":"/home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log"}`}, 0, "t3code: enabled; version 0.1.1; channel none; pin 0.1.1; service failed: failed after 5 consecutive failed starts; log /home/agent/.local/share/sandboxed-agents/tools/t3code/service/service.log\n"},
		{response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.1","channel":"","pin":"0.1.1","service_state":"stopped","service_log":"/x/service.log"}`}, 0, "t3code: enabled; version 0.1.1; channel none; pin 0.1.1; service stopped; log /x/service.log\n"},
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

// TestToolsCommandsChangeNoPodmanObjects proves tools enable, tools status, and the tool service restore issue only manager execs, never a create, start, publish, device, capability, mount, or network change, through RunWithHost and fake Podman.
func TestToolsCommandsChangeNoPodmanObjects(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[toolManagerPrefix+"enable t3code"] = response{Output: "t3code enabled: version 0.1.2 (channel latest); service running\n"}
	f.responses[toolManagerPrefix+"status t3code"] = response{Output: `{"name":"t3code","enabled":true,"installed_version":"0.1.2","channel":"latest","service_state":"running"}`}
	f.responses[toolManagerPrefix+"restore"] = response{Output: "t3code service running\n"}
	var all [][]string
	for _, args := range [][]string{{"tools", "enable", "agent01", "t3code"}, {"tools", "status", "agent01", "t3code"}, {"start", "agent01"}} {
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
