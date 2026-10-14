package main_test

import (
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const agentManagerPrefix = "exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager "

func agentExecutableHarness(t *testing.T, responses map[string]lifecycleResponse) testutil.Fakes {
	t.Helper()
	if runtime.GOOS == "windows" {
		qualified := map[string]lifecycleResponse{
			"machine list --format json": {Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`},
			"machine inspect chosen":     {Output: `[{"Name":"chosen","State":"running","Rootful":false}]`},
		}
		for key, value := range responses {
			qualified["--connection chosen "+key] = value
		}
		responses = qualified
	}
	return lifecycleHarness(t, responses)
}

func runAgentExecutable(t *testing.T, args ...string) (int, string) {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	return exitStatus(t, err), string(output)
}

func agentManagerCalls(t *testing.T, f testutil.Fakes) [][]string {
	t.Helper()
	var result [][]string
	for _, args := range testutil.Invocations(t, f.PodmanLog) {
		if len(args) > 2 && args[0] == "--connection" {
			args = args[2:]
		}
		if len(args) > 4 && args[0] == "exec" {
			result = append(result, args)
		}
	}
	return result
}

// TestAgentEnableExecutable proves a pinned enable reaches the administrative manager after its version check through the built CLI and fake Podman.
func TestAgentEnableExecutable(t *testing.T) {
	responses := ownedLifecycleResponses(true)
	responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
	responses[agentManagerPrefix+"agents enable codex --version 1.2.3 --force"] = lifecycleResponse{Output: "codex enabled: version 1.2.3 (pin 1.2.3)\n"}
	f := agentExecutableHarness(t, responses)
	status, output := runAgentExecutable(t, "agents", "enable", "demo", "codex", "--version", "1.2.3", "--force")
	if status != 0 || !strings.Contains(output, "codex enabled: version 1.2.3 (pin 1.2.3)") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	want := [][]string{
		{"exec", "--user=0:0", "sandboxed-agents.default.demo", "/usr/local/bin/sandboxed-agents-manager", "version"},
		{"exec", "--user=0:0", "sandboxed-agents.default.demo", "/usr/local/bin/sandboxed-agents-manager", "agents", "enable", "codex", "--version", "1.2.3", "--force"},
	}
	if got := agentManagerCalls(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("manager calls=%v want=%v", got, want)
	}
	assertStateQueriesOnly(t, f)
}

// TestAgentAdministrationExecutable proves disable, unpin, and enabled or absent status preserve manager results and normalize failures through the built CLI seam.
func TestAgentAdministrationExecutable(t *testing.T) {
	for _, tc := range []struct {
		verb       string
		options    []string
		answer     string
		exit, want int
		message    string
	}{
		{"disable", []string{"--force"}, "codex disabled; removed pin 1.2.3\n", 0, 0, "removed pin 1.2.3"},
		{"update", []string{"--unpin", "--force"}, "codex enabled: version 2.0.0 (pin none)\n", 0, 0, "version 2.0.0"},
		{"status", nil, `{"name":"codex","enabled":true,"installed_version":"1.2.3","pin":"1.2.3","signed_in":"unknown","session":false}`, 0, 0, "version 1.2.3; pin 1.2.3"},
		{"status", nil, `{"name":"codex","enabled":false}`, 0, 0, "codex: not enabled"},
		{"status", nil, `not-json`, 0, 1, "manager does not answer"},
		{"update", nil, "", 23, 1, "agents update demo codex"},
	} {
		t.Run(tc.verb+tc.answer, func(t *testing.T) {
			responses := ownedLifecycleResponses(true)
			responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
			request := "agents " + tc.verb + " codex"
			responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: `{"name":"codex","enabled":true,"installed_version":"1.2.3","signed_in":"unknown"}`}
			if len(tc.options) != 0 {
				request += " " + strings.Join(tc.options, " ")
			}
			responses[agentManagerPrefix+request] = lifecycleResponse{Output: tc.answer, Exit: tc.exit}
			f := agentExecutableHarness(t, responses)
			status, output := runAgentExecutable(t, append([]string{"agents", tc.verb, "demo", "codex"}, tc.options...)...)
			if status != tc.want || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want=%d %s", status, output, tc.want, tc.message)
			}
			assertStateQueriesOnly(t, f)
		})
	}
}

// TestAgentChecksPrecedeChanges proves every delivered agent command reports the first sandbox or manager failure through the executable CLI boundary.
func TestAgentChecksPrecedeChanges(t *testing.T) {
	for _, verb := range []string{"enable", "disable", "update", "status"} {
		for _, state := range []string{"stopped", "volumes only", "container owner", "volume owner", "interrupted", "manager"} {
			t.Run(verb+"/"+state, func(t *testing.T) {
				responses := ownedLifecycleResponses(false)
				message := "start demo"
				switch state {
				case "volumes only":
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
					responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
					responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
					message = "up demo"
				case "container owner":
					responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{}},"State":{"Running":false}}]`}
					message = "owner conflict"
				case "volume owner":
					responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
					responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{}}]`}
					message = "owner conflict"
				case "interrupted":
					responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
					responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
					message = "update demo"
				case "manager":
					responses = ownedLifecycleResponses(true)
					responses[agentManagerPrefix+"version"] = lifecycleResponse{Exit: 125}
					message = "manager does not answer"
				}
				f := agentExecutableHarness(t, responses)
				status, output := runAgentExecutable(t, "agents", verb, "demo", "codex")
				if status != 1 || !strings.Contains(output, message) {
					t.Fatalf("status=%d output=%s want=%s", status, output, message)
				}
				if state == "manager" && (!strings.Contains(output, "check demo") || !strings.Contains(output, "restart demo")) {
					t.Fatalf("missing recovery commands: %s", output)
				}
				for _, call := range agentManagerCalls(t, f) {
					if state != "manager" || call[len(call)-1] != "version" {
						t.Fatalf("manager mutation before refusal: %v", call)
					}
				}
				assertStateQueriesOnly(t, f)
			})
		}
	}
}

// TestUpEnablesAgentsAfterCreation proves installs follow sandbox startup and preserve it while remaining agents are attempted after a failure at the CLI and fake Podman seam.
func TestUpEnablesAgentsAfterCreation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, failure := range []string{"none", "install", "manager", "manager during install"} {
			t.Run(strings.Join([]string{map[bool]string{false: "new", true: "existing"}[existing], failure}, "/"), func(t *testing.T) {
				responses := newSandboxResponses()
				if existing {
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
				}
				responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
				responses[agentManagerPrefix+"agents enable claude"] = lifecycleResponse{Output: "claude enabled: version 1.2.3 (pin none)\n"}
				responses[agentManagerPrefix+"agents enable codex"] = lifecycleResponse{Output: "codex enabled: version 1.2.3 (pin none)\n"}
				responses[agentManagerPrefix+"agents enable copilot"] = lifecycleResponse{Output: "copilot enabled: version 1.2.3 (pin none)\n"}
				if failure == "install" {
					responses[agentManagerPrefix+"agents enable codex"] = lifecycleResponse{Exit: 1}
				}
				if failure == "manager" {
					responses[agentManagerPrefix+"version"] = lifecycleResponse{Exit: 125}
				}
				if failure == "manager during install" {
					responses[agentManagerPrefix+"agents enable codex"] = lifecycleResponse{Exit: 125}
				}
				f := lifecycleHarness(t, responses)
				status, output := runWithHost(lifecycleLinuxHost(t, f), "up", "demo", "--agents", "claude,codex,copilot")
				want := 0
				if failure != "none" {
					want = 1
				}
				if status != want {
					t.Fatalf("status=%d output=%s want=%d", status, output, want)
				}
				if strings.HasPrefix(failure, "manager") {
					if strings.Count(output, "manager does not answer") != 1 || strings.Contains(output, " enabled:") || !strings.Contains(output, "check demo") {
						t.Fatalf("manager diagnosis=%s", output)
					}
					for _, name := range []string{"claude", "codex", "copilot"} {
						if !strings.Contains(output, "agents enable demo "+name) {
							t.Fatalf("missing retry: %s", output)
						}
					}
				} else {
					for _, name := range []string{"claude", "copilot"} {
						if !strings.Contains(output, name+" enabled") {
							t.Fatalf("remaining install missing: %s", output)
						}
					}
					if failure == "install" && (!strings.Contains(output, "failed agents: codex") || !strings.Contains(output, "agents enable demo codex")) {
						t.Fatalf("failure diagnosis=%s", output)
					}
				}
				for _, call := range testutil.Invocations(t, f.PodmanLog) {
					if call[0] == "stop" || call[0] == "rm" || (len(call) > 1 && call[0] == "volume" && call[1] == "rm") {
						t.Fatalf("removed sandbox on install failure: %v", call)
					}
				}
				calls := agentManagerCalls(t, f)
				wantCalls := 4
				if failure == "manager" {
					wantCalls = 1
				}
				if failure == "manager during install" {
					wantCalls = 3
				}
				if len(calls) != wantCalls {
					t.Fatalf("manager calls=%v want count=%d", calls, wantCalls)
				}
			})
		}
	}
}

// TestListReadsAgentSelection proves list displays manager-reported enabled agents and an unavailable placeholder for unresponsive selection queries through the built CLI seam.
func TestListReadsAgentSelection(t *testing.T) {
	for _, tc := range []struct{ answer, message string }{
		{`["claude","codex"]`, "claude,codex"},
		{`[]`, "none"},
		{`invalid`, "unavailable"},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			responses := ownedLifecycleResponses(true)
			responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.demo"]}]`}
			responses["volume ls --format json"] = lifecycleResponse{Output: `[]`}
			responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
			responses[agentManagerPrefix+"agents list"] = lifecycleResponse{Output: tc.answer}
			f := agentExecutableHarness(t, responses)
			status, output := runAgentExecutable(t, "list")
			if status != 0 || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%s want=%s", status, output, tc.message)
			}
			calls := agentManagerCalls(t, f)
			if len(calls) != 2 || strings.Join(calls[1][4:], " ") != "agents list" {
				t.Fatalf("selection query=%v", calls)
			}
			assertStateQueriesOnly(t, f)
		})
	}
}

// TestAgentPinsRejectRangesBeforePodman proves only exact version pins reach host state checks at the executable CLI seam.
func TestAgentPinsRejectRangesBeforePodman(t *testing.T) {
	for _, version := range []string{"latest", "^1.2.3", "1.2.x", "file:/tmp/agent", "1.2.3;id", "1.2.3\n", "1.2.3+build.1"} {
		t.Run(version, func(t *testing.T) {
			f := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, "agents", "enable", "demo", "codex", "--version", version)
			command.Env = f.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != 2 || !strings.Contains(string(output), "requires an exact") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			assertNoExternalCalls(t, f)
		})
	}
}

// TestAgentUpdateRetryMatchesFailure proves missing selections name enable while failed installs on enabled agents name update through the executable CLI seam.
func TestAgentUpdateRetryMatchesFailure(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "install failed"}[enabled], func(t *testing.T) {
			responses := ownedLifecycleResponses(true)
			responses[agentManagerPrefix+"version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
			answer := `{"name":"codex","enabled":false}`
			message := "agents enable demo codex"
			if enabled {
				answer = `{"name":"codex","enabled":true,"installed_version":"1.2.3","signed_in":"unknown"}`
				message = "agents update demo codex"
			}
			responses[agentManagerPrefix+"agents status codex"] = lifecycleResponse{Output: answer}
			responses[agentManagerPrefix+"agents update codex"] = lifecycleResponse{Exit: 1, Stderr: "npm installation failed\n"}
			f := agentExecutableHarness(t, responses)
			status, output := runAgentExecutable(t, "agents", "update", "demo", "codex")
			if status != 1 || !strings.Contains(output, message) {
				t.Fatalf("status=%d output=%s want=%s", status, output, message)
			}
			if enabled && strings.Contains(output, "agents enable") {
				t.Fatalf("incorrect enable guidance: %s", output)
			}
			if !enabled {
				for _, call := range agentManagerCalls(t, f) {
					if strings.Join(call[4:], " ") == "agents update codex" {
						t.Fatalf("attempted update of missing agent: %v", call)
					}
				}
			}
		})
	}
}
