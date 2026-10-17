package cli_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const sandboxBackup = "sandboxed-agents-backup.default.agent01"

var sandboxVolumes = []string{shellContainer + ".workspace", shellContainer + ".home", shellContainer + ".ssh"}

// sandboxStateFixture prepares a running sandbox fixture with scripted shell output at the
// public CLI seam.
func sandboxStateFixture(t *testing.T) *fixture {
	t.Helper()
	f := fingerprintFixture(t)
	f.host.Stdin = strings.NewReader("")
	f.responses[shellExec] = response{Output: "shell output\n"}
	return f
}

// containerInspectAnswer formats a container-inspect fixture with supplied labels and running
// state.
func containerInspectAnswer(labels, running string) string {
	return `[{"Config":{"Labels":` + labels + `},"State":{"Running":` + running + `}}]`
}

// volumeInspectAnswer formats a volume-inspect fixture with the supplied labels.
func volumeInspectAnswer(labels string) string {
	return `[{"Labels":` + labels + `}]`
}

// configureSandboxQueries scripts owned sandbox and absent-volume responses for the requested
// group and connection.
func configureSandboxQueries(f *fixture, group, connection string) {
	container := "sandboxed-agents." + group + ".agent01"
	labels := `{"io.sandboxed-agents.owner":"` + group + `"}`
	f.responses[connection+"container inspect "+container] = response{Output: containerInspectAnswer(labels, "true")}
	f.responses[connection+"container inspect sandboxed-agents-backup."+group+".agent01"] = response{Output: containerInspectAnswer(labels, "false")}
	for _, suffix := range []string{".workspace", ".home", ".ssh"} {
		f.responses[connection+"volume exists "+container+suffix] = response{Exit: 1}
		f.responses[connection+"volume inspect "+container+suffix] = response{Output: volumeInspectAnswer(labels)}
	}
	f.responses[connection+"container exists sandboxed-agents-backup."+group+".agent01"] = response{Exit: 1}
}

// sandboxObjectCalls builds the expected existence and inspect queries for the supplied sandbox
// object state.
func sandboxObjectCalls(container, backup bool, volumes ...string) [][]string {
	calls := [][]string{{"container", "exists", shellContainer}}
	if container {
		calls = append(calls, []string{"container", "inspect", shellContainer})
	}
	for _, volume := range sandboxVolumes {
		calls = append(calls, []string{"volume", "exists", volume})
		for _, existing := range volumes {
			if volume == existing {
				calls = append(calls, []string{"volume", "inspect", volume})
			}
		}
	}
	calls = append(calls, []string{"container", "exists", sandboxBackup})
	if backup {
		calls = append(calls, []string{"container", "inspect", sandboxBackup})
	}
	return calls
}

// sandboxExistenceCalls returns the expected object queries when the sandbox has no existing
// objects.
func sandboxExistenceCalls() [][]string {
	return sandboxObjectCalls(false, false)
}

// sandboxQueryCalls returns the expected object queries for an existing sandbox container
// without volumes or backup.
func sandboxQueryCalls() [][]string {
	return sandboxObjectCalls(true, false)
}

// assertSandboxRefusal checks that the public CLI refuses sandbox state without host-file reads,
// SSH calls, or Podman actions.
func assertSandboxRefusal(t *testing.T, f *fixture, command string, messages ...string) [][]string {
	t.Helper()
	f.host.ReadFile = func(path string) ([]byte, error) {
		t.Fatalf("refused sandbox read host file: %s", path)
		return nil, nil
	}
	status, stdout, stderr, calls := runCommand(t, f, command, "agent01")
	if status != 1 || stdout != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	for _, message := range messages {
		if !strings.Contains(stderr, message) {
			t.Fatalf("stderr=%q; want %q", stderr, message)
		}
	}
	if strings.Contains(stderr, "owner conflict") && (strings.Contains(stderr, "update agent01") || strings.Contains(stderr, "up agent01") || strings.Contains(stderr, "interrupted update")) {
		t.Fatalf("owner conflict suggested later recovery: %q", stderr)
	}
	for _, call := range calls {
		if call[0] == "exec" {
			t.Fatalf("refused sandbox executed action: %v", call)
		}
	}
	noMutation(t, calls)
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
	return calls
}

// TestSandboxCommandsRefuseForeignBackup proves backup ownership outranks interrupted-update recovery without actions or host reads through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseForeignBackup(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		t.Run(command, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses["container exists "+sandboxBackup] = response{}
			f.responses["container inspect "+sandboxBackup] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"other"}},"State":{"Running":false}}]`}
			calls := assertSandboxRefusal(t, f, command, "owner conflict", sandboxBackup, "Podman")
			want := sandboxObjectCalls(true, true)
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestSandboxCommandsRefuseForeignVolumesWithoutContainer proves every foreign remaining volume without a container outranks a backup or missing sandbox refusal through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseForeignVolumesWithoutContainer(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, backup := range []bool{false, true} {
			for _, labels := range []string{`{}`, `{"io.sandboxed-agents.owner":"other"}`} {
				t.Run(command+"/backup-"+map[bool]string{false: "absent", true: "present"}[backup]+"/"+labels, func(t *testing.T) {
					f := sandboxStateFixture(t)
					f.responses["container exists "+shellContainer] = response{Exit: 1}
					if backup {
						f.responses["container exists "+sandboxBackup] = response{}
					}
					for _, volume := range sandboxVolumes {
						f.responses["volume exists "+volume] = response{}
						f.responses["volume inspect "+volume] = response{Output: volumeInspectAnswer(labels)}
					}
					messages := append([]string{"owner conflict", "Podman"}, sandboxVolumes...)
					calls := assertSandboxRefusal(t, f, command, messages...)
					want := sandboxObjectCalls(false, backup, sandboxVolumes...)
					if !reflect.DeepEqual(calls, want) {
						t.Fatalf("calls=%v want=%v", calls, want)
					}
				})
			}
		}
	}
}

// TestSandboxCommandsLookUpObjectsBeforeOwnerConflicts proves foreign container ownership outranks a backup after all sandbox object queries through RunWithHost and fake Podman.
func TestSandboxCommandsLookUpObjectsBeforeOwnerConflicts(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		t.Run(command, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"other"}`, "false")}
			f.responses["container exists "+sandboxBackup] = response{}
			calls := assertSandboxRefusal(t, f, command, "owner conflict", shellContainer, "Podman")
			want := sandboxObjectCalls(true, true)
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestSandboxCommandsExplainRemainingVolumes proves shell and fingerprint direct users to up NAME after checking ownership of clean remaining volumes through RunWithHost and fake Podman.
func TestSandboxCommandsExplainRemainingVolumes(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, volume := range sandboxVolumes {
			t.Run(command+"/"+volume, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.responses["container exists "+shellContainer] = response{Exit: 1}
				f.responses["volume exists "+volume] = response{}
				calls := assertSandboxRefusal(t, f, command, `sandbox "agent01"`, "up agent01", "adopt", "volumes")
				want := sandboxObjectCalls(false, false, volume)
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
			})
		}
	}
}

// TestSandboxCommandsRefuseOwnerConflicts proves shell and fingerprint report every container, backup or existing volume with missing, empty or foreign ownership before recovery and running-state checks through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseOwnerConflicts(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, labels := range []string{`{}`, `null`, `{"io.sandboxed-agents.owner":""}`, `{"io.sandboxed-agents.owner":"other"}`} {
			for _, object := range append([]string{shellContainer, sandboxBackup, "all"}, sandboxVolumes...) {
				t.Run(command+"/"+object+"/"+labels, func(t *testing.T) {
					f := sandboxStateFixture(t)
					messages := []string{"owner conflict", "Podman", "remove or rename"}
					if object == shellContainer || object == "all" {
						f.responses[shellInspect] = response{Output: containerInspectAnswer(labels, "false")}
						messages = append(messages, shellContainer)
					}
					if object == sandboxBackup || object == "all" {
						f.responses["container inspect "+sandboxBackup] = response{Output: containerInspectAnswer(labels, "false")}
						messages = append(messages, sandboxBackup)
					}
					var existing []string
					for _, volume := range sandboxVolumes {
						if object == volume || object == "all" {
							f.responses["volume exists "+volume] = response{}
							f.responses["volume inspect "+volume] = response{Output: volumeInspectAnswer(labels)}
							messages = append(messages, volume)
							existing = append(existing, volume)
						}
					}
					f.responses["container exists "+sandboxBackup] = response{}
					want := sandboxObjectCalls(true, true, existing...)
					calls := assertSandboxRefusal(t, f, command, messages...)
					if !reflect.DeepEqual(calls, want) {
						t.Fatalf("calls=%v want=%v", calls, want)
					}
				})
			}
		}
	}
}

// TestSandboxCommandsRefuseInterruptedUpdates proves shell and fingerprint suggest update NAME before checking stopped state when a backup container exists through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseInterruptedUpdates(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		t.Run(command, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses["container exists "+sandboxBackup] = response{}
			f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
			calls := assertSandboxRefusal(t, f, command, `sandbox "agent01"`, "interrupted update", "update agent01")
			want := sandboxObjectCalls(true, true)
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestSandboxCommandsRefuseBackupWithoutContainer proves shell and fingerprint refuse an interrupted update without a container, with absent or clean remaining volumes, through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseBackupWithoutContainer(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, volumes := range []bool{false, true} {
			t.Run(command+"/volumes-"+map[bool]string{false: "absent", true: "present"}[volumes], func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.responses["container exists "+shellContainer] = response{Exit: 1}
				f.responses["container exists "+sandboxBackup] = response{}
				if volumes {
					for _, volume := range sandboxVolumes {
						f.responses["volume exists "+volume] = response{}
					}
				}
				calls := assertSandboxRefusal(t, f, command, `sandbox "agent01"`, "interrupted update", "update agent01")
				want := sandboxObjectCalls(false, true)
				if volumes {
					want = sandboxObjectCalls(false, true, sandboxVolumes...)
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
			})
		}
	}
}

// TestSandboxCommandsCheckRunningStateAfterOwnership proves correctly owned stopped sandboxes suggest start and correctly owned running sandboxes perform only their intended action through RunWithHost and fake Podman.
func TestSandboxCommandsCheckRunningStateAfterOwnership(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, running := range []string{"false", "true"} {
			t.Run(command+"/"+running, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, running)}
				want := sandboxObjectCalls(true, false, sandboxVolumes...)
				for _, volume := range sandboxVolumes {
					f.responses["volume exists "+volume] = response{}
					f.responses["volume inspect "+volume] = response{Output: volumeInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`)}
				}
				var calls [][]string
				if running == "false" {
					calls = assertSandboxRefusal(t, f, command, `sandbox "agent01" is stopped`, "sandboxed-agents start agent01")
				} else {
					status, stdout, stderr, invocations := runCommand(t, f, command, "agent01")
					calls = invocations
					expected := "shell output\n"
					if command == "fingerprint" {
						expected = fingerprintOutput
						for _, key := range fingerprintKeys {
							want = append(want, strings.Fields(fingerprintRead(key.path)))
						}
					} else {
						want = append(want, strings.Fields(shellExec))
					}
					if status != 0 || stdout != expected || stderr != "" {
						t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
					}
					noMutation(t, calls)
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
			})
		}
	}
}

// TestSandboxCommandsRefuseFailedStateQueries proves unsuccessful existence and owner queries or malformed owner JSON refuse both commands without actions through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseFailedStateQueries(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, key := range []string{"container exists " + shellContainer, shellInspect, "container inspect " + sandboxBackup, "volume exists " + sandboxVolumes[0], "volume inspect " + sandboxVolumes[0], "container exists " + sandboxBackup} {
			for _, malformed := range []bool{false, true} {
				if malformed && !strings.Contains(key, " inspect ") {
					continue
				}
				t.Run(command+"/"+key+"/"+map[bool]string{true: "malformed", false: "failure"}[malformed], func(t *testing.T) {
					f := sandboxStateFixture(t)
					if key == "container inspect "+sandboxBackup {
						f.responses["container exists "+sandboxBackup] = response{}
					}
					if strings.HasPrefix(key, "volume inspect") {
						f.responses["volume exists "+sandboxVolumes[0]] = response{}
					}
					message := "query failed"
					f.responses[key] = response{Stderr: message, Exit: 125}
					if malformed {
						message = "invalid inspect answer"
						output := containerInspectAnswer(`{"io.sandboxed-agents.owner":17}`, "true")
						if strings.HasPrefix(key, "volume inspect") {
							output = volumeInspectAnswer(`{"io.sandboxed-agents.owner":17}`)
						}
						f.responses[key] = response{Output: output}
					}
					calls := assertSandboxRefusal(t, f, command, message)
					if strings.Join(calls[len(calls)-1], " ") != key {
						t.Fatalf("continued after query failure: %v", calls)
					}
				})
			}
		}
	}
}

// TestSandboxCommandsRefuseFailedRemainingVolumeQueries proves shell and fingerprint cannot report unknown or adoptable sandboxes when a remaining-volume query fails through RunWithHost and fake Podman.
func TestSandboxCommandsRefuseFailedRemainingVolumeQueries(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, volume := range sandboxVolumes {
			t.Run(command+"/"+volume, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.responses["container exists "+shellContainer] = response{Exit: 1}
				f.responses["volume exists "+volume] = response{Stderr: "remaining volume query failed", Exit: 125}
				calls := assertSandboxRefusal(t, f, command, "remaining volume query failed")
				if !reflect.DeepEqual(calls[len(calls)-1], []string{"volume", "exists", volume}) {
					t.Fatalf("continued after query failure: %v", calls)
				}
			})
		}
	}
}

// TestWindowsSandboxCommandsSelectTarget proves shell and fingerprint select the sole or default machine with only local list and inspect queries before sandbox lookup through RunWithHost and fake Podman.
func TestWindowsSandboxCommandsSelectTarget(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, list := range []string{
			`[{"Name":"chosen","VMType":"wsl"}]`,
			`[{"Name":"other","VMType":"wsl"},{"Name":"chosen","Default":true,"VMType":"wsl"}]`,
		} {
			t.Run(command+"/"+list, func(t *testing.T) {
				f := windowsHost(t)
				f.host.WindowsBuild = func() (uint32, error) { t.Fatal("unexpected prerequisite Windows build query"); return 0, nil }
				configureSandboxQueries(f, "default", "--connection chosen ")
				f.responses["machine list --format json"] = response{Output: list}
				f.responses["--connection chosen container exists "+shellContainer] = response{Exit: 1}
				status, output, calls := f.run(t, command, "agent01")
				if status != 1 || output != "sandbox \"agent01\" does not exist in this controller group\n" {
					t.Fatalf("status=%d output=%q", status, output)
				}
				want := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
				for _, call := range sandboxExistenceCalls() {
					want = append(want, append([]string{"--connection", "chosen"}, call...))
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
			})
		}
	}
}

// TestWindowsSandboxCommandsRefuseInvalidTargets proves shell and fingerprint refuse every ADR-0006 selection failure before sandbox lookup using only machine list and inspect through RunWithHost and fake Podman.
func TestWindowsSandboxCommandsRefuseInvalidTargets(t *testing.T) {
	for _, command := range []string{"shell", "fingerprint"} {
		for _, tc := range []struct {
			name, query, message string
			value                response
			inspections          int
		}{
			{"missing", "machine list --format json", "a Podman machine is required", response{Output: `[]`}, 0},
			{"ambiguous", "machine list --format json", "select one default Podman machine", response{Output: `[{"Name":"one"},{"Name":"two"}]`}, 0},
			{"multiple-defaults", "machine list --format json", "select one default Podman machine", response{Output: `[{"Name":"one","Default":true},{"Name":"two","Default":true}]`}, 0},
			{"list-failure", "machine list --format json", "list unavailable", response{Stderr: "list unavailable", Exit: 125}, 0},
			{"list-malformed", "machine list --format json", "read Podman machine list", response{Output: `{`}, 0},
			{"empty-name", "machine list --format json", "invalid Podman machine name", response{Output: `[{"Name":"","VMType":"wsl"}]`}, 0},
			{"option-name", "machine list --format json", "invalid Podman machine name", response{Output: `[{"Name":"-bad","VMType":"wsl"}]`}, 0},
			{"newline-name", "machine list --format json", "invalid Podman machine name", response{Output: `[{"Name":"bad\nname","VMType":"wsl"}]`}, 0},
			{"carriage-return-name", "machine list --format json", "invalid Podman machine name", response{Output: `[{"Name":"bad\rname","VMType":"wsl"}]`}, 0},
			{"nul-name", "machine list --format json", "invalid Podman machine name", response{Output: `[{"Name":"bad\u0000name","VMType":"wsl"}]`}, 0},
			{"inspect-failure", "machine inspect chosen", "inspect unavailable", response{Stderr: "inspect unavailable", Exit: 125}, 1},
			{"inspect-malformed", "machine inspect chosen", "read Podman machine inspect", response{Output: `{`}, 1},
			{"inspect-wrong-name", "machine inspect chosen", "read Podman machine inspect", response{Output: `[{"Name":"other","State":"running","Rootful":false}]`}, 1},
			{"stopped", "machine inspect chosen", "a running Podman machine is required", response{Output: `[{"Name":"chosen","State":"stopped","Rootful":false}]`}, 1},
			{"provider", "machine list --format json", "a WSL2 Podman machine is required", response{Output: `[{"Name":"chosen","VMType":"qemu"}]`}, 1},
			{"rootful", "machine inspect chosen", "a rootless Podman machine is required", response{Output: `[{"Name":"chosen","State":"running","Rootful":true}]`}, 1},
			{"rootful-unreported", "machine inspect chosen", "Rootful must be reported", response{Output: `[{"Name":"chosen","State":"running"}]`}, 1},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				f := windowsHost(t)
				f.responses[tc.query] = tc.value
				status, output, calls := f.run(t, command, "agent01")
				if status != 1 || !strings.Contains(output, tc.message) {
					t.Fatalf("status=%d output=%q", status, output)
				}
				want := [][]string{{"machine", "list", "--format", "json"}}
				if tc.inspections == 1 {
					want = append(want, []string{"machine", "inspect", "chosen"})
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("calls=%v want=%v", calls, want)
				}
			})
		}
	}
}
