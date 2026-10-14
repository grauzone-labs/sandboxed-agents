package main_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func newSandboxResponses() map[string]lifecycleResponse {
	responses := ownedLifecycleResponses(true)
	responses["--version"] = lifecycleResponse{Output: "podman version 4.4.0"}
	responses["info --format json"] = lifecycleResponse{Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`}
	responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
	responses["ps --all --format json"] = lifecycleResponse{Output: `[]`}
	responses["image exists *"] = lifecycleResponse{}
	responses["volume create *"] = lifecycleResponse{}
	responses["create *"] = lifecycleResponse{}
	return responses
}

// TestUpAdoptsPartialVolumesBesideWorkspaceBind proves up names adopted, created, and unused kept volumes through the CLI boundary and fake Podman.
func TestUpAdoptsPartialVolumesBesideWorkspaceBind(t *testing.T) {
	responses := newSandboxResponses()
	for _, kind := range []string{"workspace", "home"} {
		name := "sandboxed-agents.default.demo." + kind
		responses["volume exists "+name] = lifecycleResponse{}
		responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	}
	fakes := lifecycleHarness(t, responses)
	status, output := runWithHost(lifecycleLinuxHost(t, fakes), "up", "demo", t.TempDir())
	for _, message := range []string{"unused kept workspace volume sandboxed-agents.default.demo.workspace", "adopted volume sandboxed-agents.default.demo.home", "created volume sandboxed-agents.default.demo.ssh"} {
		if status != 0 || !strings.Contains(output, message) {
			t.Fatalf("status=%d output=%s; want %s", status, output, message)
		}
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "source=sandboxed-agents.default.demo.workspace") || (len(call) > 1 && call[0] == "volume" && call[1] == "create" && call[len(call)-1] != "sandboxed-agents.default.demo.ssh") {
			t.Fatalf("used or recreated kept workspace/home: %v", call)
		}
	}
}

func runWithHost(host cli.Host, args ...string) (int, string) {
	var output bytes.Buffer
	status := cli.RunWithHost(args, &output, &output, host)
	return status, output.String()
}

// TestUpAdoptsKeptVolumes proves up reuses and names owned volumes without recreating them through the CLI boundary and fake Podman.
func TestUpAdoptsKeptVolumes(t *testing.T) {
	responses := newSandboxResponses()
	for _, kind := range []string{"workspace", "home", "ssh"} {
		name := "sandboxed-agents.default.demo." + kind
		responses["volume exists "+name] = lifecycleResponse{}
		responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	}
	fakes := lifecycleHarness(t, responses)
	status, output := runWithHost(lifecycleLinuxHost(t, fakes), "up", "demo")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, kind := range []string{"workspace", "home", "ssh"} {
		if !strings.Contains(output, "adopted volume sandboxed-agents.default.demo."+kind) {
			t.Fatalf("missing adoption: %s", output)
		}
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 1 && call[0] == "volume" && call[1] == "create" {
			t.Fatalf("recreated kept volume: %v", call)
		}
	}
}

// TestListShowsPartialVolumesOnly proves list names a partial kept volume set with no container details through the CLI boundary and fake Podman.
func TestListShowsPartialVolumesOnly(t *testing.T) {
	responses := newSandboxResponses()
	responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.other.hidden"]}]`}
	responses["volume ls --format json"] = lifecycleResponse{Output: `[{"Name":"sandboxed-agents.default.demo.workspace"},{"Name":"sandboxed-agents.other.hidden.home"}]`}
	responses["volume exists sandboxed-agents.default.demo.workspace"] = lifecycleResponse{}
	responses["volume inspect sandboxed-agents.default.demo.workspace"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	fakes := lifecycleHarness(t, responses)
	status, output := lifecycleRun("list")
	for _, message := range []string{"demo", "volumes only", "volume", "sandboxed-agents.default.demo.workspace"} {
		if status != 0 || !strings.Contains(output, message) {
			t.Fatalf("status=%d output=%s; want %s", status, output, message)
		}
	}
	if strings.Contains(output, "hidden") || strings.Contains(output, "2222") || strings.Contains(output, "unavailable") {
		t.Fatalf("foreign/container details in volumes-only row: %s", output)
	}
	assertStateQueriesOnly(t, fakes)
}

func assertStateQueriesOnly(t *testing.T, fakes testutil.Fakes) {
	t.Helper()
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && (call[0] == "create" || call[0] == "build" || call[0] == "start" || call[0] == "stop" || call[0] == "restart" || call[0] == "rm" || (call[0] == "volume" && len(call) > 1 && (call[1] == "create" || call[1] == "rm"))) {
			t.Fatalf("state check changed Podman objects: %v", call)
		}
	}
	if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) > 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestListClassifiesSandboxStates proves owner conflicts override interrupted updates and unavailable agents have a placeholder through the CLI boundary.
func TestListClassifiesSandboxStates(t *testing.T) {
	for _, state := range []string{"stopped", "manager unavailable", "update interrupted", "owner conflict", "backup owner conflict"} {
		t.Run(state, func(t *testing.T) {
			responses := ownedLifecycleResponses(state != "stopped")
			responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.demo","sandboxed-agents-backup.default.demo"]}]`}
			responses["volume ls --format json"] = lifecycleResponse{Output: `[]`}
			responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Exit: 125}
			want := "stopped"
			if state != "stopped" {
				want = "running"
			}
			if strings.Contains(state, "interrupted") || strings.Contains(state, "conflict") {
				responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
				responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
				want = "update interrupted"
			}
			if state == "owner conflict" {
				responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
				responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{}}]`}
				want = "owner conflict"
			}
			if state == "backup owner conflict" {
				responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{}}}]`}
				want = "owner conflict"
			}
			fakes := lifecycleHarness(t, responses)
			status, output := lifecycleRun("list")
			if status != 0 || !strings.Contains(output, want) || strings.Count(output, "\ndemo ") != 1 || !strings.Contains(output, "unavailable") {
				t.Fatalf("status=%d output=%s; want one %s row", status, output, want)
			}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if call[0] == "exec" && state != "manager unavailable" {
					t.Fatalf("queried manager in %s state: %v", state, call)
				}
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}

// TestCheckReportsOwnerAndBackupProblems proves check NAME reports conflicting objects and interrupted updates without changes through the CLI boundary.
func TestCheckReportsOwnerAndBackupProblems(t *testing.T) {
	responses := ownedLifecycleResponses(false)
	responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
	responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{}}]`}
	responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
	responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"foreign"}}}]`}
	fakes := lifecycleHarness(t, responses)
	status, output := lifecycleRun("check", "demo")
	for _, message := range []string{"owner conflict", "sandboxed-agents.default.demo.home", "sandboxed-agents-backup.default.demo", "Podman", "interrupted update"} {
		if status != 1 || !strings.Contains(output, message) {
			t.Fatalf("status=%d output=%s; want %s", status, output, message)
		}
	}
	assertStateQueriesOnly(t, fakes)
}

// TestCheckNamesKeptAndUnusedWorkspaceVolumes proves partial owned volumes exit zero and binds name unused workspace volumes while broader diagnostics remain pending through the CLI boundary.
func TestCheckNamesKeptAndUnusedWorkspaceVolumes(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial volumes", true: "workspace bind"}[bound], func(t *testing.T) {
			responses := ownedLifecycleResponses(false)
			if !bound {
				responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
			} else {
				responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":false},"Mounts":[{"Type":"bind","Destination":"/workspace","Source":"/project"}]}]`}
			}
			responses["volume exists sandboxed-agents.default.demo.workspace"] = lifecycleResponse{}
			responses["volume inspect sandboxed-agents.default.demo.workspace"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
			fakes := lifecycleHarness(t, responses)
			status, output := lifecycleRun("check", "demo")
			want := "volumes only"
			if bound {
				want = "unused workspace volume"
			}
			wantStatus := 0
			if bound {
				wantStatus = 1 // Full container diagnostics remain a separate story.
			}
			if status != wantStatus || !strings.Contains(output, want) || !strings.Contains(output, "sandboxed-agents.default.demo.workspace; owner default") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}

func namedCommandForms() [][]string {
	return [][]string{
		{"up", "demo"}, {"start", "demo"}, {"stop", "demo", "--force"}, {"restart", "demo", "--force"}, {"remove", "demo", "--force"},
		{"shell", "demo"}, {"fingerprint", "demo"}, {"ssh-config", "demo"}, {"ssh-config", "demo", "--install"}, {"ssh-config", "demo", "--remove"},
		{"update", "demo"}, {"integrations", "login", "demo", "azure"}, {"integrations", "login", "demo", "azdo", "--organization", "https://example.com/org"},
		{"integrations", "config", "demo", "git", "identity"}, {"integrations", "config", "demo", "git", "credentials"},
	}
}

// TestNamedCommandsRefuseConflictingVolumeBeforeLaterChecks proves every accepted named command form refuses an owned sandbox's foreign volume before backup, stopped, or action checks through the CLI boundary.
func TestNamedCommandsRefuseConflictingVolumeBeforeLaterChecks(t *testing.T) {
	for _, args := range namedCommandForms() {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			responses := newSandboxResponses()
			responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
			responses["container inspect sandboxed-agents.default.demo"] = ownedLifecycleResponses(false)["container inspect sandboxed-agents.default.demo"]
			responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
			responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"foreign"}}]`}
			responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
			responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
			fakes := lifecycleHarness(t, responses)
			status, output := runWithHost(lifecycleLinuxHost(t, fakes), args...)
			if status != 1 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, "sandboxed-agents.default.demo.home") || !strings.Contains(output, "Podman") || strings.Contains(output, "update demo") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}

// TestRemoveVolumesKeepsForeignVolumeBesideOwnedSandbox proves the explicit removal exception deletes only the owned container and volumes and reports failure through the CLI boundary.
func TestRemoveVolumesKeepsForeignVolumeBesideOwnedSandbox(t *testing.T) {
	responses := ownedLifecycleResponses(false)
	for _, kind := range []string{"workspace", "home", "ssh"} {
		name := "sandboxed-agents.default.demo." + kind
		owner := "default"
		if kind == "home" {
			owner = "foreign"
		}
		responses["volume exists "+name] = lifecycleResponse{}
		responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
		responses["volume rm "+name] = lifecycleResponse{}
	}
	fakes := lifecycleHarness(t, responses)
	status, output := lifecycleRun("remove", "demo", "--volumes")
	if status != 1 || !strings.Contains(output, "removed sandbox demo") || !strings.Contains(output, "kept volume sandboxed-agents.default.demo.home") || !strings.Contains(output, "Podman") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	var removed []string
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if call[0] == "rm" || (len(call) > 1 && call[0] == "volume" && call[1] == "rm") {
			removed = append(removed, strings.Join(call, " "))
		}
	}
	want := "rm sandboxed-agents.default.demo|volume rm sandboxed-agents.default.demo.workspace|volume rm sandboxed-agents.default.demo.ssh"
	if strings.Join(removed, "|") != want {
		t.Fatalf("removed=%v; want %s", removed, want)
	}
}

// TestRemoveOwnedVolumesWithoutContainer proves explicit cleanup removes only owned remaining volumes while start, stop, restart, and owner conflicts refuse changes through RunWithHost and fake Podman.
func TestRemoveOwnedVolumesWithoutContainer(t *testing.T) {
	for _, kinds := range [][]string{{"home"}, {"workspace", "home", "ssh"}} {
		for _, args := range [][]string{{"remove", "demo", "--volumes"}, {"start", "demo"}, {"stop", "demo"}, {"restart", "demo"}} {
			for _, owner := range []string{"default", "", "foreign"} {
				t.Run(strings.Join(kinds, ",")+"/"+strings.Join(args, " ")+"/owner="+owner, func(t *testing.T) {
					responses := newSandboxResponses()
					var wantRemoved []string
					for _, kind := range kinds {
						name := "sandboxed-agents.default.demo." + kind
						responses["volume exists "+name] = lifecycleResponse{}
						responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
						responses["volume rm "+name] = lifecycleResponse{}
						wantRemoved = append(wantRemoved, "volume rm "+name)
					}
					fakes := lifecycleHarness(t, responses)
					status, output := runWithHost(lifecycleLinuxHost(t, fakes), args...)
					if owner != "default" || len(args) == 2 {
						want := "up demo"
						if owner != "default" {
							want = "owner conflict"
						}
						if status != 1 || !strings.Contains(output, want) {
							t.Fatalf("status=%d output=%s; want %s", status, output, want)
						}
						assertStateQueriesOnly(t, fakes)
						return
					}
					if status != 0 || strings.Contains(output, "removed sandbox") {
						t.Fatalf("status=%d output=%s; want volume cleanup without container removal", status, output)
					}
					var removed []string
					for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
						if call[0] == "exec" || len(lifecycleMutations([][]string{call})) > 0 {
							t.Fatalf("accessed absent container: %v", call)
						}
						if len(call) > 1 && call[0] == "volume" && call[1] == "rm" {
							removed = append(removed, strings.Join(call, " "))
						}
					}
					if strings.Join(removed, "|") != strings.Join(wantRemoved, "|") {
						t.Fatalf("removed=%v; want %v", removed, wantRemoved)
					}
					for _, kind := range kinds {
						if !strings.Contains(output, "removed volume sandboxed-agents.default.demo."+kind) {
							t.Fatalf("missing volume removal report: %s", output)
						}
					}
				})
			}
		}
	}
}

// TestStartRefusesOccupiedRecordedPort proves start refuses a real listener on its recorded port without choosing a substitute or changing the sandbox through the CLI boundary.
func TestStartRefusesOccupiedRecordedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	responses := ownedLifecycleResponses(false)
	responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"` + port + `"}},"State":{"Running":false}}]`}
	fakes := lifecycleHarness(t, responses)
	status, output := runWithHost(cli.Host{Platform: "linux"}, "start", "demo")
	if status != 1 || !strings.Contains(output, "SSH port "+port) || !strings.Contains(output, "unavailable") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	assertStateQueriesOnly(t, fakes)
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if call[0] == "ps" {
			t.Fatalf("allocated a replacement port: %v", call)
		}
	}
}

// TestUpExistingPortMustMatchBeforeStart proves up reuses a stopped sandbox's recorded port and refuses a differing port without starting through the CLI boundary.
func TestUpExistingPortMustMatchBeforeStart(t *testing.T) {
	for _, requested := range []string{"", "2300", "2301"} {
		t.Run("port="+requested, func(t *testing.T) {
			responses := newSandboxResponses()
			responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
			responses["container inspect sandboxed-agents.default.demo"] = ownedLifecycleResponses(false)["container inspect sandboxed-agents.default.demo"]
			fakes := lifecycleHarness(t, responses)
			host := lifecycleLinuxHost(t, fakes)
			host.PortAvailable = func(uint16) error { return nil }
			args := []string{"up", "demo"}
			if requested != "" {
				args = append(args, "--port", requested)
			}
			status, output := runWithHost(host, args...)
			if requested == "2301" {
				if status != 1 || !strings.Contains(output, "recorded SSH port 2300") || !strings.Contains(output, "2301") || !strings.Contains(output, "remove") || !strings.Contains(output, "up") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				assertStateQueriesOnly(t, fakes)
			} else {
				if status != 0 || !strings.Contains(output, "sandbox demo is running") || len(lifecycleMutations(testutil.Invocations(t, fakes.PodmanLog))) != 1 {
					t.Fatalf("status=%d output=%s", status, output)
				}
			}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if call[0] == "ps" || call[0] == "image" || call[0] == "build" || call[0] == "create" {
					t.Fatalf("reconfigured existing sandbox: %v", call)
				}
			}
		})
	}
}

// TestNamedCommandsRefuseInterruptedUpdates proves backup-only and container-plus-backup states refuse every command except update NAME, check NAME, and list through the CLI boundary.
func TestNamedCommandsRefuseInterruptedUpdates(t *testing.T) {
	for _, args := range namedCommandForms() {
		if args[0] == "update" {
			continue
		}
		for _, container := range []bool{false, true} {
			t.Run(strings.Join(args, " ")+"/container="+strconv.FormatBool(container), func(t *testing.T) {
				responses := newSandboxResponses()
				if container {
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
				}
				responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
				responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
				fakes := lifecycleHarness(t, responses)
				status, output := runWithHost(lifecycleLinuxHost(t, fakes), args...)
				if status != 1 || !strings.Contains(output, "interrupted update") || !strings.Contains(output, "update demo") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				assertStateQueriesOnly(t, fakes)
			})
		}
	}
}

// TestLifecycleLockPrecedesEverySandboxState proves a taken lock hides unknown, volumes-only, owner-conflict, and interrupted-update states after preflight through the CLI boundary.
func TestLifecycleLockPrecedesEverySandboxState(t *testing.T) {
	for _, state := range []string{"unknown", "volumes only", "owner conflict", "interrupted update"} {
		for _, verb := range []string{"up", "start", "stop", "restart", "remove", "update"} {
			t.Run(verb+"/"+state, func(t *testing.T) {
				responses := newSandboxResponses()
				if state == "volumes only" || state == "owner conflict" {
					responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
					owner := "default"
					if state == "owner conflict" {
						owner = "foreign"
					}
					responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				}
				if state == "interrupted update" {
					responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
				}
				fakes := lifecycleHarness(t, responses)
				holdLifecycleLock(t, os.Getenv("XDG_STATE_HOME"), "sandboxed-agents.default.demo")
				status, output := runWithHost(lifecycleLinuxHost(t, fakes), verb, "demo")
				if status != 1 || !strings.HasPrefix(output, "step 3:") || !strings.Contains(output, "another lifecycle command is in progress") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
					if call[0] != "--version" && call[0] != "info" {
						t.Fatalf("looked up sandbox before lock: %v", call)
					}
				}
			})
		}
	}
}

// TestUsageAndPreflightPrecedeTakenLifecycleLock proves usage and path errors make no Podman calls and preflight failure wins over a held lock through the CLI boundary.
func TestUsageAndPreflightPrecedeTakenLifecycleLock(t *testing.T) {
	for _, args := range [][]string{{"up", "demo", "--port", "0"}, {"up", "demo", "MISSING"}, {"start", "demo", "--bad"}, {"stop", "demo", "--bad"}, {"restart", "demo", "--bad"}, {"remove", "demo", "--bad"}, {"up", "demo"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			responses := newSandboxResponses()
			responses["--version"] = lifecycleResponse{Exit: 125, Output: "preflight failure sentinel"}
			fakes := lifecycleHarness(t, responses)
			holdLifecycleLock(t, os.Getenv("XDG_STATE_HOME"), "sandboxed-agents.default.demo")
			if args[len(args)-1] == "MISSING" {
				args = append([]string{}, args...)
				args[len(args)-1] = filepath.Join(t.TempDir(), "missing")
			}
			status, output := runWithHost(lifecycleLinuxHost(t, fakes), args...)
			if len(args) == 2 {
				if status != 1 || !strings.HasPrefix(output, "step 2:") || !strings.Contains(output, "preflight failure sentinel") || strings.Contains(output, "step 3:") {
					t.Fatalf("status=%d output=%s", status, output)
				}
			} else {
				if status != 2 || !strings.HasPrefix(output, "step 1:") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				assertNoExternalCalls(t, fakes)
			}
		})
	}
}

func listWithLaterHealthyResponses() map[string]lifecycleResponse {
	responses := newSandboxResponses()
	responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.zhealthy","sandboxed-agents.default.demo"]}]`}
	responses["volume ls --format json"] = lifecycleResponse{Output: `[]`}
	for key, response := range ownedLifecycleResponses(true) {
		responses[strings.ReplaceAll(key, ".demo", ".zhealthy")] = response
	}
	responses["container inspect sandboxed-agents.default.zhealthy"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2300"},"Image":"localhost/sandboxed-agents:none-abc"},"State":{"Running":true}}]`}
	responses["exec --user=0:0 sandboxed-agents.default.zhealthy /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
	return responses
}

func listRow(t *testing.T, output, name string) []string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return fields
		}
	}
	t.Fatalf("missing row for %s: %s", name, output)
	return nil
}

// TestListRetainsInspectionProblems proves failed or malformed inspections retain a problem row with placeholders, preserve known owner conflicts, and continue to a later healthy sandbox through RunWithHost and fake Podman.
func TestListRetainsInspectionProblems(t *testing.T) {
	for _, object := range []string{"container", "volume", "backup", "owner conflict"} {
		for _, malformed := range []bool{false, true} {
			t.Run(object+"/malformed="+strconv.FormatBool(malformed), func(t *testing.T) {
				responses := listWithLaterHealthyResponses()
				key := "container inspect sandboxed-agents.default.demo"
				switch object {
				case "container":
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
				case "owner conflict":
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
					responses[key] = lifecycleResponse{Output: `[{"Config":{"Labels":{}}}]`}
					fallthrough
				case "volume":
					responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
					key = "volume inspect sandboxed-agents.default.demo.home"
				case "backup":
					responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
					key = "container inspect sandboxed-agents-backup.default.demo"
				}
				problem := "inspection failure sentinel"
				responses[key] = lifecycleResponse{Exit: 125, Output: problem}
				if malformed {
					responses[key] = lifecycleResponse{Output: `not-json`}
					problem = "invalid inspect answer"
				}
				fakes := lifecycleHarness(t, responses)
				status, output := runWithHost(lifecycleLinuxHost(t, fakes), "list")
				if status != 0 {
					t.Fatalf("status=%d output=%s; want problem row and later healthy row", status, output)
				}
				row := listRow(t, output, "demo")
				if object == "owner conflict" {
					if !strings.Contains(strings.Join(row, " "), "owner conflict") || !strings.Contains(strings.Join(row, " "), "Podman") {
						t.Fatalf("owner conflict lost priority: %v", row)
					}
				} else if len(row) < 8 || strings.Join(row[:7], " ") != "demo - - - - - -" || !strings.Contains(strings.Join(row[7:], " "), problem) {
					t.Fatalf("problem/placeholders missing: %v; want %s", row, problem)
				}
				row = listRow(t, output, "zhealthy")
				if len(row) < 7 || strings.Join(row[:6], " ") != "zhealthy running 2300 - none none" || strings.Index(output, "\ndemo ") > strings.Index(output, "\nzhealthy ") {
					t.Fatalf("later healthy details missing or out of order: %s", output)
				}
				for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
					if call[0] == "exec" && call[2] != "sandboxed-agents.default.zhealthy" {
						t.Fatalf("queried unhealthy manager: %v", call)
					}
				}
				assertStateQueriesOnly(t, fakes)
			})
		}
	}
}

// TestListRetainsMissingRunningState proves an incomplete state retains container details, state and agent placeholders, and a problem while later healthy sandboxes are listed through RunWithHost and fake Podman.
func TestListRetainsMissingRunningState(t *testing.T) {
	responses := listWithLaterHealthyResponses()
	responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
	responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2300"},"Image":"localhost/sandboxed-agents:none-abc"}}]`}
	fakes := lifecycleHarness(t, responses)
	status, output := runWithHost(lifecycleLinuxHost(t, fakes), "list")
	if status != 0 {
		t.Fatalf("status=%d output=%s; want incomplete and healthy rows", status, output)
	}
	row := listRow(t, output, "demo")
	if len(row) < 7 || strings.Join(row[:6], " ") != "demo - 2300 - none unavailable" || !strings.Contains(strings.Join(row[6:], " "), "read container sandboxed-agents.default.demo: running state is missing") {
		t.Fatalf("incomplete state row=%v", row)
	}
	row = listRow(t, output, "zhealthy")
	if len(row) < 7 || strings.Join(row[:6], " ") != "zhealthy running 2300 - none none" {
		t.Fatalf("later healthy row=%v", row)
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if call[0] == "exec" && call[2] != "sandboxed-agents.default.zhealthy" {
			t.Fatalf("queried manager without running state: %v", call)
		}
	}
	assertStateQueriesOnly(t, fakes)
}

// TestListOwnerConflictWinsOverMissingRunningState proves a foreign container is listed as an owner conflict with its Podman name even when its state is incomplete through the CLI boundary.
func TestListOwnerConflictWinsOverMissingRunningState(t *testing.T) {
	responses := ownedLifecycleResponses(false)
	responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.demo"]}]`}
	responses["volume ls --format json"] = lifecycleResponse{Output: `[]`}
	responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{}}}]`}
	fakes := lifecycleHarness(t, responses)
	status, output := lifecycleRun("list")
	if status != 0 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, "sandboxed-agents.default.demo") || !strings.Contains(output, "Podman") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	assertStateQueriesOnly(t, fakes)
}

// TestControllerGroupsIsolateEveryNamedCommand proves another group's sandbox is invisible to list, unknown to named commands, and permits same-name creation through the CLI boundary.
func TestControllerGroupsIsolateEveryNamedCommand(t *testing.T) {
	for _, args := range append(namedCommandForms(), []string{"check", "demo"}, []string{"list"}) {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			responses := newSandboxResponses()
			for key, response := range newSandboxResponses() {
				key = strings.ReplaceAll(key, ".default.demo", ".team.demo")
				response.Output = strings.ReplaceAll(response.Output, `"default"`, `"team"`)
				responses[key] = response
			}
			responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{}
			responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.demo"],"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2300"}}]`}
			responses["volume ls --format json"] = lifecycleResponse{Output: `[{"Name":"sandboxed-agents.default.demo.home"}]`}
			fakes := lifecycleHarness(t, responses)
			t.Setenv("SANDBOXED_AGENTS_GROUP", "team")
			host := lifecycleLinuxHost(t, fakes)
			host.PortAvailable = func(uint16) error { return nil }
			status, output := runWithHost(host, args...)
			switch args[0] {
			case "list":
				if status != 0 || strings.Contains(output, "demo") {
					t.Fatalf("foreign sandbox listed: status=%d output=%s", status, output)
				}
			case "up":
				if status != 0 || !strings.Contains(output, "sandbox demo running") {
					t.Fatalf("same-name creation: status=%d output=%s", status, output)
				}
			default:
				if status != 1 || !strings.Contains(output, "does not exist in this controller group") {
					t.Fatalf("foreign sandbox known: status=%d output=%s", status, output)
				}
			}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if strings.Contains(strings.Join(call, " "), "sandboxed-agents.default.demo") {
					t.Fatalf("accessed foreign group object: %v", call)
				}
			}
		})
	}
}

// TestUpRefusesOccupiedExplicitPortBeforeCreation proves a real host listener or another group's recorded port, including ownerless backups, prevents creation and builds through the CLI boundary.
func TestUpRefusesOccupiedExplicitPortBeforeCreation(t *testing.T) {
	for _, source := range []string{"host listener", "foreign record", "ownerless record", "ownerless backup record"} {
		t.Run(source, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
			responses := newSandboxResponses()
			if source != "host listener" {
				name, owner := "sandboxed-agents.other.busy", `,"io.sandboxed-agents.owner":"other"`
				if strings.HasPrefix(source, "ownerless") {
					owner = ""
				}
				if source == "ownerless backup record" {
					name = "sandboxed-agents-backup.other.busy"
				}
				responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["` + name + `"],"Labels":{"io.sandboxed-agents.port":"` + port + `"` + owner + `}}]`}
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
			fakes := lifecycleHarness(t, responses)
			host := lifecycleLinuxHost(t, fakes)
			if source != "host listener" {
				host.PortAvailable = func(uint16) error { return nil }
			}
			status, output := runWithHost(host, "up", "demo", "--port", port)
			want := "already recorded"
			if source == "host listener" {
				want = "unavailable"
			}
			if status != 1 || !strings.Contains(output, "SSH port "+port) || !strings.Contains(output, want) {
				t.Fatalf("status=%d output=%s", status, output)
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}

// TestUpRefusesUnownedKeptVolumesIncludingUnusedWorkspace proves missing and foreign owners on every kept volume stop up before creation even beside a workspace bind through the CLI boundary.
func TestUpRefusesUnownedKeptVolumesIncludingUnusedWorkspace(t *testing.T) {
	for _, owner := range []string{"", "foreign"} {
		for _, kind := range []string{"workspace", "home", "ssh"} {
			t.Run(kind+"/owner="+owner, func(t *testing.T) {
				responses := newSandboxResponses()
				name := "sandboxed-agents.default.demo." + kind
				responses["volume exists "+name] = lifecycleResponse{}
				responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				fakes := lifecycleHarness(t, responses)
				status, output := runWithHost(lifecycleLinuxHost(t, fakes), "up", "demo", t.TempDir())
				if status != 1 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, name) || !strings.Contains(output, "Podman") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				assertStateQueriesOnly(t, fakes)
			})
		}
	}
}

// TestListResponsiveSandboxShowsContainerDetails proves list displays the recorded port, bind workspace and image toolchains when the running manager answers through the CLI boundary.
func TestListResponsiveSandboxShowsContainerDetails(t *testing.T) {
	responses := ownedLifecycleResponses(true)
	responses["ps --all --format json"] = lifecycleResponse{Output: `[{"Names":["sandboxed-agents.default.demo"]}]`}
	responses["volume ls --format json"] = lifecycleResponse{Output: `[]`}
	responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2300"},"Image":"localhost/sandboxed-agents:none-415c3113ed82008b40e2fe7cb30194620f516112597733bbcaed46dc030739e0"},"State":{"Running":true},"Mounts":[{"Type":"bind","Source":"/project","Destination":"/workspace"}]}]`}
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
	fakes := lifecycleHarness(t, responses)
	status, output := lifecycleRun("list")
	for _, message := range []string{"running", "2300", "bind", "none"} {
		if status != 0 || !strings.Contains(output, message) || strings.Contains(output, "unavailable") {
			t.Fatalf("status=%d output=%s; want %s", status, output, message)
		}
	}
	assertStateQueriesOnly(t, fakes)
}

// TestUpdateAndCheckPermitOwnedBackupState proves update NAME reaches its deferred action and check NAME reports the owned backup without restoring it through the CLI boundary.
func TestUpdateAndCheckPermitOwnedBackupState(t *testing.T) {
	for _, verb := range []string{"update", "check"} {
		t.Run(verb, func(t *testing.T) {
			responses := newSandboxResponses()
			responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
			responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
			fakes := lifecycleHarness(t, responses)
			status, output := runWithHost(lifecycleLinuxHost(t, fakes), verb, "demo")
			if verb == "update" {
				if status != 1 || output != "update: not implemented\n" {
					t.Fatalf("status=%d output=%s", status, output)
				}
			} else if status != 1 || !strings.Contains(output, "sandboxed-agents-backup.default.demo") || !strings.Contains(output, "update demo") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}

// TestFailedUpNeverDeletesAdoptedVolumes proves create and start failures roll back only newly created objects and preserve adopted home data through the CLI boundary.
func TestFailedUpNeverDeletesAdoptedVolumes(t *testing.T) {
	for _, failure := range []string{"create", "start"} {
		t.Run(failure, func(t *testing.T) {
			responses := newSandboxResponses()
			responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
			responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
			responses["volume rm *"] = lifecycleResponse{}
			responses["container rm --force sandboxed-agents.default.demo"] = lifecycleResponse{}
			key := "create *"
			if failure == "start" {
				key = "start sandboxed-agents.default.demo"
			}
			responses[key] = lifecycleResponse{Exit: 125}
			fakes := lifecycleHarness(t, responses)
			status, output := runWithHost(lifecycleLinuxHost(t, fakes), "up", "demo")
			if status != 1 || !strings.Contains(output, "adopted volume sandboxed-agents.default.demo.home") || strings.Contains(output, "sandbox demo running") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			var removed []string
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if len(call) > 1 && call[0] == "volume" && (call[1] == "create" || call[1] == "rm") && call[len(call)-1] == "sandboxed-agents.default.demo.home" {
					t.Fatalf("changed adopted home data: %v", call)
				}
				if len(call) > 1 && call[0] == "volume" && call[1] == "rm" {
					removed = append(removed, call[len(call)-1])
				}
			}
			if strings.Join(removed, ",") != "sandboxed-agents.default.demo.workspace,sandboxed-agents.default.demo.ssh" {
				t.Fatalf("new volume rollback=%v", removed)
			}
		})
	}
}

// TestCheckRunningSandboxCannotClaimUnverifiedHealth proves check queries an owned running manager and fails while broader container diagnostics remain incomplete through the CLI boundary.
func TestCheckRunningSandboxCannotClaimUnverifiedHealth(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run("manager="+strconv.FormatBool(available), func(t *testing.T) {
			responses := ownedLifecycleResponses(true)
			key := "exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"
			responses[key] = lifecycleResponse{Exit: 125}
			if available {
				responses[key] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
			}
			fakes := lifecycleHarness(t, responses)
			status, output := lifecycleRun("check", "demo")
			if status != 1 || !strings.Contains(output, "diagnostics are not implemented") || (!available && !strings.Contains(output, "manager does not answer")) {
				t.Fatalf("status=%d output=%s", status, output)
			}
			queried := false
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				queried = queried || strings.Join(call, " ") == key
			}
			if !queried {
				t.Fatal("owned running manager was not checked")
			}
			assertStateQueriesOnly(t, fakes)
		})
	}
}
