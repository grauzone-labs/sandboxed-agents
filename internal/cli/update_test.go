package cli_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func updateFixture(t *testing.T) *fixture {
	t.Helper()
	f := linuxHost(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{}
	f.responses["container inspect sandboxed-agents.default.demo"] = response{Output: `[{"Image":"sha256:current","Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"none","io.sandboxed-agents.port":"2222"}},"State":{"Running":true},"HostConfig":{"Memory":8589934592,"CpuQuota":400000,"CpuPeriod":100000,"PidsLimit":2048,"ShmSize":1073741824},"Mounts":[{"Type":"volume","Name":"sandboxed-agents.default.demo.workspace","Destination":"/workspace"},{"Type":"volume","Name":"sandboxed-agents.default.demo.home","Destination":"/home/agent"},{"Type":"volume","Name":"sandboxed-agents.default.demo.ssh","Destination":"/etc/ssh"}]}]`}
	f.responses["volume exists *"] = response{}
	f.responses["volume inspect *"] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	f.responses["container exists sandboxed-agents-backup.default.demo"] = response{Exit: 1}
	f.responses["image exists "+baseTag(t)] = response{}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:current"}]`}
	return f
}

// TestUpdateCurrentImageIsUntouched proves an already current sandbox exits successfully without mutations or manager queries through the CLI boundary.
func TestUpdateCurrentImageIsUntouched(t *testing.T) {
	f := updateFixture(t)
	f.responses["exec *"] = response{Output: "codex\n"}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 0 || !strings.Contains(output, "already up to date") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	noMutation(t, calls)
	for _, call := range calls {
		if call[0] == "exec" {
			t.Fatalf("queried sessions: %v", call)
		}
	}
}

func replacementFixture(t *testing.T) *fixture {
	t.Helper()
	f := updateFixture(t)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, `"sha256:current"`, `"sha256:old"`, 1)
	f.responses[key] = r
	for _, call := range []string{"rename sandboxed-agents.default.demo sandboxed-agents-backup.default.demo", "create *", "stop sandboxed-agents-backup.default.demo", "start sandboxed-agents.default.demo", "rm sandboxed-agents-backup.default.demo"} {
		f.responses[call] = response{}
	}
	f.responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions"] = response{}
	f.responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = response{Output: "sandboxed-agents-manager development\n"}
	t.Setenv("TEST_SSH_OUTPUT", "debug1: SSH2_MSG_NEWKEYS received\n")
	t.Setenv("TEST_SSH_EXIT", "255")
	t.Setenv("TEST_SSH_KNOWN_HOSTS", "[127.0.0.1]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n")
	return f
}

// TestUpdateReplacesSandbox proves a replacement keeps volumes, port and limits and stops the old container before starting the new one through the executable CLI and fake processes.
func TestUpdateReplacesSandbox(t *testing.T) {
	f := replacementFixture(t)
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 0 || !strings.Contains(output, "sandbox demo updated") {
		t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
	}
	var changes []string
	for _, call := range calls {
		switch call[0] {
		case "rename", "create", "stop", "start", "rm":
			changes = append(changes, call[0])
		}
		if call[0] == "create" {
			joined := strings.Join(call, " ")
			for _, want := range []string{"--memory 8589934592", "--cpus 4", "--pids-limit 2048", "--shm-size 1073741824", "127.0.0.1:2222:22/tcp", "io.sandboxed-agents.update-was-running=true", "source=sandboxed-agents.default.demo.workspace", "source=sandboxed-agents.default.demo.home", "source=sandboxed-agents.default.demo.ssh", baseTag(t)} {
				if !strings.Contains(joined, want) {
					t.Errorf("create lacks %s: %v", want, call)
				}
			}
		}
	}
	if strings.Join(changes, ",") != "rename,create,stop,start,rm" {
		t.Fatalf("changes=%v", changes)
	}
}

// TestUpdateRollsBackFailures proves create, start, and readiness failures restore the old name and running state without deleting volumes through the CLI process boundary.
func TestUpdateRollsBackFailures(t *testing.T) {
	for _, stage := range []string{"create", "start", "readiness"} {
		t.Run(stage, func(t *testing.T) {
			f := replacementFixture(t)
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			switch stage {
			case "create":
				f.responses["create *"] = response{Exit: 125, Output: "create failed"}
			case "start":
				f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "start failed", FailOnce: true}
			case "readiness":
				t.Setenv("TEST_SSH_KNOWN_HOSTS", "invalid\n")
			}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "restored") {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			restore := -1
			for i, call := range calls {
				if strings.Join(call, " ") == "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo" {
					restore = i
				}
				if len(call) > 1 && call[0] == "volume" && call[1] != "exists" && call[1] != "inspect" {
					t.Fatalf("changed volume: %v", call)
				}
			}
			if restore < 0 {
				t.Fatal("no restore")
			}
			// Before the old container stops there is no need to restart it.
			if stage != "create" && (restore+1 >= len(calls) || strings.Join(calls[restore+1], " ") != "start sandboxed-agents.default.demo") {
				t.Fatalf("old sandbox not restarted: %v", calls)
			}
		})
	}
}

// TestUpdateCleanupFailuresAreNotRolledBack proves committed replacements report cleanup failures and preserve the prescribed running state through the CLI boundary.
func TestUpdateCleanupFailuresAreNotRolledBack(t *testing.T) {
	for _, tc := range []struct {
		stage   string
		stopped bool
	}{{"rename", false}, {"backup removal", false}, {"backup removal", true}, {"final stop", true}} {
		t.Run(tc.stage+map[bool]string{true: " stopped", false: " running"}[tc.stopped], func(t *testing.T) {
			f := replacementFixture(t)
			if tc.stopped {
				r := f.responses["container inspect sandboxed-agents.default.demo"]
				r.Output = strings.ReplaceAll(r.Output, `"Running":true`, `"Running":false`)
				f.responses["container inspect sandboxed-agents.default.demo"] = r
			}
			failed := "rename sandboxed-agents.default.demo sandboxed-agents-backup.default.demo"
			if tc.stage == "backup removal" {
				failed = "rm sandboxed-agents-backup.default.demo"
			}
			if tc.stage == "final stop" {
				failed = "stop sandboxed-agents.default.demo"
			}
			f.responses[failed] = response{Exit: 125, Output: "injected failure"}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if tc.stage != "rename" {
				for _, want := range []string{"update succeeded", "still running"} {
					if (want != "still running" || tc.stopped) && !strings.Contains(output, want) {
						t.Errorf("output lacks %q: %s", want, output)
					}
				}
				if tc.stage == "backup removal" {
					for _, want := range []string{"update interrupted", "other commands refuse", "remove the stopped backup: podman rm sandboxed-agents-backup.default.demo", "update demo cleans up"} {
						if !strings.Contains(output, want) {
							t.Errorf("output lacks %q: %s", want, output)
						}
					}
				}
			}
			last := strings.Join(calls[len(calls)-1], " ")
			if last != failed {
				t.Fatalf("unexpected rollback/action after committed failure: %v", calls)
			}
		})
	}
}

// TestUpdateSessionGuardAndRollbackReports proves forced replacements query sessions, preserve them until stop and name ended sessions after rollback through the CLI seam.
func TestUpdateSessionGuardAndRollbackReports(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			for _, fail := range []string{"", "create", "start"} {
				t.Run(fmt.Sprintf("unknown=%t/force=%t/fail=%s", unknown, force, fail), func(t *testing.T) {
					f := replacementFixture(t)
					answer := response{Output: "codex\nclaude\n"}
					if unknown {
						answer = response{Exit: 125, Output: "manager unavailable"}
					}
					f.responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions"] = answer
					f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
					f.responses["rm --force sandboxed-agents.default.demo"] = response{}
					if fail == "create" {
						f.responses["create *"] = response{Exit: 125}
					}
					if fail == "start" {
						f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, FailOnce: true}
					}
					args := []string{"update", "demo"}
					if force {
						args = append(args, "--force")
					}
					status, output, calls := runUpExecutable(t, f, args...)
					want := 1
					if force && fail == "" {
						want = 0
					}
					if status != want {
						t.Fatalf("status=%d output=%s", status, output)
					}
					joined := strings.Join(flatCalls(calls), "\n")
					if !strings.Contains(joined, "/usr/local/bin/sandboxed-agents-manager sessions") {
						t.Fatal("manager was not asked")
					}
					if !force {
						noMutation(t, calls)
						for _, message := range []string{"--force", map[bool]string{true: "cannot rule out running sessions", false: "codex, claude"}[unknown]} {
							if !strings.Contains(output, message) {
								t.Fatalf("missing %s: %s", message, output)
							}
						}
					} else if fail == "create" {
						if strings.Contains(output, "ended") || strings.Contains(joined, "stop sandboxed-agents-backup") {
							t.Fatalf("sessions ended too soon: %s %s", output, joined)
						}
					} else {
						message := "ended agent sessions: codex, claude"
						if unknown {
							message = "sessions that may have been running were ended and cannot be named"
						}
						if !strings.Contains(output, message) {
							t.Fatalf("missing ended report: %s", output)
						}
					}
				})
			}
		}
	}
}

func flatCalls(calls [][]string) []string {
	out := make([]string, len(calls))
	for i, call := range calls {
		out[i] = strings.Join(call, " ")
	}
	return out
}

// TestUpdateStoppedSandboxAndToolchains proves replacement retains or changes the exact toolchain set, workspace bind and stopped state through CLI creation arguments and rollback order.
func TestUpdateStoppedSandboxAndToolchains(t *testing.T) {
	for _, set := range []string{"", "none", "native,dotnet"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("set=%s/failure=%t", set, failed), func(t *testing.T) {
				f := replacementFixture(t)
				key := "container inspect sandboxed-agents.default.demo"
				r := f.responses[key]
				r.Output = strings.ReplaceAll(r.Output, `"Running":true`, `"Running":false`)
				r.Output = strings.ReplaceAll(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"azure"`)
				r.Output = strings.Replace(r.Output, `"Type":"volume","Name":"sandboxed-agents.default.demo.workspace"`, `"Type":"bind","Source":"/projects/my work"`, 1)
				f.responses[key] = r
				selected := "azure"
				if set != "" {
					selected = set
				}
				if selected == "native,dotnet" {
					selected = "dotnet,native"
				}
				tag := strings.Replace(baseTag(t), ":none-", ":"+strings.ReplaceAll(selected, ",", "-")+"-", 1)
				f.responses["image exists "+tag] = response{}
				f.responses["image inspect "+tag] = response{Output: `[{"Id":"new-toolchain","Labels":{"io.sandboxed-agents.base-image":"sha256:current"}}]`}
				f.responses["stop sandboxed-agents.default.demo"] = response{}
				f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
				f.responses["rm --force sandboxed-agents.default.demo"] = response{}
				if failed {
					f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125}
				}
				args := []string{"update", "demo"}
				if set != "" {
					args = append(args, "--with", set)
				}
				status, output, calls := runUpExecutable(t, f, args...)
				if status != map[bool]int{false: 0, true: 1}[failed] {
					t.Fatalf("status=%d output=%s", status, output)
				}
				for _, call := range calls {
					joined := strings.Join(call, " ")
					if call[0] == "create" {
						for _, want := range []string{"io.sandboxed-agents.toolchains=" + selected, "io.sandboxed-agents.update-was-running=false", "type=bind,source=/projects/my work,destination=/workspace", tag} {
							if !strings.Contains(joined, want) {
								t.Errorf("missing %s: %s", want, joined)
							}
						}
					}
					if call[0] == "exec" && call[len(call)-1] == "sessions" {
						t.Fatal("queried sessions on stopped sandbox")
					}
					if joined == "stop sandboxed-agents-backup.default.demo" {
						t.Fatal("stopped an already stopped backup")
					}
				}
				joined := flatCalls(calls)
				if failed {
					if joined[len(joined)-1] != "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo" {
						t.Fatalf("rollback did not leave original stopped: %v", calls)
					}
				} else {
					if strings.Join(joined[len(joined)-2:], ";") != "rm sandboxed-agents-backup.default.demo;stop sandboxed-agents.default.demo" {
						t.Fatalf("incorrect success stop order: %v", calls)
					}
				}
			})
		}
	}
}

// TestUpdateBuildFailurePreservesSandbox proves builds finish before session checks or mutations and failed builds leave a running sandbox untouched through the CLI seam.
func TestUpdateBuildFailurePreservesSandbox(t *testing.T) {
	f := replacementFixture(t)
	f.responses["image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["build *"] = response{Exit: 125, Output: "build failed"}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "build failed") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		switch call[0] {
		case "rename", "create", "start", "stop", "rm", "exec":
			t.Fatalf("sandbox touched before build success: %v", call)
		}
	}
}

// TestUpdateRebuildsStaleToolchainBase proves stale image lineage forces a rebuild before session queries or replacement even when the image ID matches through the CLI seam.
func TestUpdateRebuildsStaleToolchainBase(t *testing.T) {
	f := replacementFixture(t)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.ReplaceAll(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"native"`)
	f.responses[key] = r
	f.responses["image exists localhost/sandboxed-agents:native-*"] = response{}
	f.responses["image inspect localhost/sandboxed-agents:native-*"] = response{Output: `[{"Id":"sha256:old","Labels":{"io.sandboxed-agents.base-image":"previous-base"}}]`}
	f.responses["build *"] = response{}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	built := false
	for _, call := range calls {
		if call[0] == "build" {
			built = true
			if !strings.Contains(strings.Join(call, " "), "BASE_IMAGE=sha256:current") {
				t.Fatalf("wrong base: %v", call)
			}
		}
		if (call[0] == "rename" || call[0] == "exec") && !built {
			t.Fatalf("sandbox touched before rebuild: %v", call)
		}
	}
	if !built {
		t.Fatal("stale image reused")
	}
}

// TestUpdatePreservesSSHFilesAndAuthorization proves stop/start/update retain the same SSH volume and leave host keys, managed config and authorization untouched at the CLI process and filesystem seams.
func TestUpdatePreservesSSHFilesAndAuthorization(t *testing.T) {
	f := replacementFixture(t)
	groupDirectory := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "group-default", "ssh")
	hash := sha256.Sum256([]byte("demo"))
	directory := filepath.Join(groupDirectory, fmt.Sprintf("sandbox-%x", hash))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id_ed25519", "id_ed25519.pub", "known_hosts", "entry"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("original "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if status, output, _ := runUpExecutable(t, f, "ssh-config", "demo"); status != 0 || output != "original entry" {
		t.Fatalf("SSH config did not read the fixture entry: status=%d output=%s fixture=%s", status, output, directory)
	}
	f.responses["stop sandboxed-agents.default.demo"] = response{}
	if status, output, _ := runUpExecutable(t, f, "stop", "demo"); status != 0 {
		t.Fatalf("stop=%d %s", status, output)
	}
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.ReplaceAll(r.Output, `"Running":true`, `"Running":false`)
	f.responses[key] = r
	if status, output, _ := runUpExecutable(t, f, "start", "demo"); status != 0 {
		t.Fatalf("start=%d %s", status, output)
	}
	r.Output = strings.ReplaceAll(r.Output, `"Running":false`, `"Running":true`)
	f.responses[key] = r
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 0 {
		t.Fatalf("update=%d %s", status, output)
	}
	for _, name := range []string{"id_ed25519", "id_ed25519.pub", "known_hosts", "entry"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != "original "+name {
			t.Fatalf("host file %s changed: %s %v", name, data, err)
		}
	}
	for _, call := range calls {
		if call[0] == "volume" && call[1] != "exists" && call[1] != "inspect" {
			t.Fatalf("volume changed: %v", call)
		}
		if call[0] == "exec" && call[len(call)-1] != "sessions" && call[len(call)-1] != "version" {
			t.Fatalf("changed in-sandbox keys: %v", call)
		}
	}
}

// TestWindowsUpdateUsesPreflightTarget proves image, replacement, rollback, readiness, retry cleanup and manual cleanup guidance keep the selected connection through RunWithHost and fake Podman.
func TestWindowsUpdateUsesPreflightTarget(t *testing.T) {
	for _, failure := range []string{"", "start", "backup removal", "cleanup"} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			f := replacementFixture(t)
			if failure == "cleanup" {
				f = cleanupFixture(t, false, false)
			}
			win := windowsHost(t)
			for key, value := range f.responses {
				if key != "--version" && key != "info --format json" {
					win.responses["--connection chosen "+key] = value
				}
			}
			if failure == "start" {
				win.responses["--connection chosen start sandboxed-agents.default.demo"] = response{Exit: 125, FailOnce: true}
				win.responses["--connection chosen rm --force sandboxed-agents.default.demo"] = response{}
				win.responses["--connection chosen rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			}
			if failure == "backup removal" {
				win.responses["--connection chosen rm sandboxed-agents-backup.default.demo"] = response{Exit: 125, Output: "backup removal failed"}
			}
			// windowsHost resets the fake SSH environment; restore successful readiness.
			t.Setenv("TEST_SSH_OUTPUT", "debug1: SSH2_MSG_NEWKEYS received\n")
			t.Setenv("TEST_SSH_EXIT", "255")
			status, output, calls := win.run(t, "update", "demo")
			wantStatus := 0
			if failure != "" && failure != "cleanup" {
				wantStatus = 1
			}
			if status != wantStatus {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if failure == "backup removal" && !strings.Contains(output, "remove the stopped backup in PowerShell: podman --connection 'chosen' rm sandboxed-agents-backup.default.demo") {
				t.Fatalf("cleanup guidance lacks the selected target: %s", output)
			}
			for _, call := range calls {
				if call[0] != "machine" && call[0] != "--version" && (len(call) < 3 || call[0] != "--connection" || call[1] != "chosen") {
					t.Fatalf("unbound target: %v", call)
				}
			}
		})
	}
}

// TestUpdateHoldsLockThroughReplacement proves a competing stop is refused before lookup while replacement or retry cleanup waits for a container to start through concurrent CLI processes.
func TestUpdateHoldsLockThroughReplacement(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanup), func(t *testing.T) {
			f := replacementFixture(t)
			if cleanup {
				f = cleanupFixture(t, false, false)
			}
			ready := filepath.Join(t.TempDir(), "starting")
			release := ready + ".release"
			f.responses["start sandboxed-agents.default.demo"] = response{ReadyFile: ready, WaitFor: release}
			done := make(chan string, 1)
			go func() {
				status, output, _ := runUpExecutable(t, f, "update", "demo")
				done <- fmt.Sprintf("%d:%s", status, output)
			}()
			defer os.WriteFile(release, nil, 0600)
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("update did not reach start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			status, output, _ := runUpExecutable(t, f, "stop", "demo")
			if status != 1 || !strings.Contains(output, "step 3:") || !strings.Contains(output, "retry") {
				t.Fatalf("competing stop=%d %s", status, output)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if !strings.HasPrefix(result, "0:") {
					t.Fatal(result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("update did not finish")
			}
		})
	}
}

// TestUpdateRefusesUnavailableSandboxBeforeBuild proves unknown, volumes-only, foreign-owner and interrupted states cause no build or mutation through the executable CLI boundary.
func TestUpdateRefusesUnavailableSandboxBeforeBuild(t *testing.T) {
	for _, state := range []string{"unknown", "volumes only", "foreign container", "foreign volume", "foreign backup", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			f := replacementFixture(t)
			want := "owner conflict"
			switch state {
			case "unknown":
				f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
				f.responses["volume exists *"] = response{Exit: 1}
				want = "does not exist"
			case "volumes only":
				f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
				want = "up demo"
			case "foreign container":
				r := f.responses["container inspect sandboxed-agents.default.demo"]
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`, 1)
				f.responses["container inspect sandboxed-agents.default.demo"] = r
			case "foreign volume":
				f.responses["volume inspect *"] = response{Output: `[{"Labels":{}}]`}
			case "foreign backup", "interrupted":
				f.responses["container exists sandboxed-agents-backup.default.demo"] = response{}
				owner := "foreign"
				if state == "interrupted" {
					owner = "default"
					want = "backup running state is missing"
				}
				f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}}]`}
			}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, want) {
				t.Fatalf("status=%d output=%s", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestUpdateRollbackFailureStopsSafely proves cleanup or restoration errors stop rollback before unsafe follow-on actions and never delete volumes through the CLI process seam.
func TestUpdateRollbackFailureStopsSafely(t *testing.T) {
	for _, failed := range []string{"stop sandboxed-agents-backup.default.demo", "rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo", "start sandboxed-agents.default.demo"} {
		t.Run(failed, func(t *testing.T) {
			f := replacementFixture(t)
			f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, FailOnce: true, Output: "new start failed"}
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			f.responses[failed] = response{Exit: 125, Output: "injected rollback failure"}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "injected rollback failure") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if strings.HasPrefix(failed, "stop ") {
				for _, call := range calls {
					if strings.Join(call, " ") == "start sandboxed-agents.default.demo" { // only the restored old container may start
						joined := flatCalls(calls)
						previous := strings.Join(joined, "\n")
						if strings.Index(previous, "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo") > strings.Index(previous, "start sandboxed-agents.default.demo") {
							t.Fatal("new container started after failed old stop")
						}
					}
				}
			} else if strings.Join(calls[len(calls)-1], " ") != failed {
				t.Fatalf("continued after rollback failure: %v", calls)
			}
			for _, call := range calls {
				if call[0] == "volume" && call[1] != "exists" && call[1] != "inspect" {
					t.Fatalf("volume mutated: %v", call)
				}
			}
		})
	}
}

// TestListInterruptedUpdateSuppressesOutdated proves a backup's interrupted state outranks image staleness and performs no image query through the CLI seam.
func TestListInterruptedUpdateSuppressesOutdated(t *testing.T) {
	f := replacementFixture(t)
	f.responses["ps --all --format json"] = response{Output: `[{"Names":["sandboxed-agents.default.demo","sandboxed-agents-backup.default.demo"]}]`}
	f.responses["volume ls --format json"] = response{Output: `[]`}
	f.responses["container exists sandboxed-agents-backup.default.demo"] = response{}
	f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
	status, output, calls := runUpExecutable(t, f, "list")
	if status != 0 || !strings.Contains(output, "update interrupted") || strings.Contains(output, "outdated") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		if call[0] == "image" {
			t.Fatalf("queried outdated image for interrupted update: %v", call)
		}
	}
}
