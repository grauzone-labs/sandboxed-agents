package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// batchUpdateFixture scripts outdated sandboxes and inventory responses for batch-update CLI
// tests.
func batchUpdateFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	f := replacementFixture(t)
	template := make(map[string]response, len(f.responses))
	for key, value := range f.responses {
		template[key] = value
	}
	var inventory []map[string]any
	for _, name := range names {
		for key, value := range template {
			if strings.Contains(key, ".demo") {
				value.Output = strings.ReplaceAll(value.Output, ".demo", "."+name)
				f.responses[strings.ReplaceAll(key, ".demo", "."+name)] = value
			}
		}
		inventory = append(inventory, map[string]any{"Names": []string{"sandboxed-agents.default." + name}})
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	f.responses["ps --all --format json"] = response{Output: string(data)}
	f.responses["volume ls --format json"] = response{Output: "[]"}
	return f
}

// TestUpdateAllContinuesAfterFailure proves a failed sandbox does not prevent another replacement or current-image reporting through the executable CLI seam.
func TestUpdateAllContinuesAfterFailure(t *testing.T) {
	f := batchUpdateFixture(t, "demo", "next", "current")
	f.responses["rename sandboxed-agents.default.demo sandboxed-agents-backup.default.demo"] = response{Exit: 125, Output: "rename failed"}
	key := "container inspect sandboxed-agents.default.current"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, "sha256:old", "sha256:current", 1)
	f.responses[key] = r
	status, output, calls := runUpExecutable(t, f, "update", "--all")
	if status != 1 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, want := range []string{"rename failed", "sandbox next updated", "sandbox current already up to date", "1 updated", "1 current", "1 not updated"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q: %s", want, output)
		}
	}
	preflights := 0
	for _, call := range calls {
		joined := strings.Join(call, " ")
		if joined == "info --format json" {
			preflights++
		}
		if (call[0] == "rename" || call[0] == "exec") && strings.Contains(joined, ".current") {
			t.Errorf("current sandbox touched: %v", call)
		}
	}
	if preflights != 1 {
		t.Fatalf("preflights=%d calls=%v", preflights, calls)
	}
}

// TestUpdateAllContinuesAfterRollback proves create and start failures restore one sandbox and rollback failures preserve its backup without preventing the next update through the executable CLI seam.
func TestUpdateAllContinuesAfterRollback(t *testing.T) {
	for _, stage := range []string{"create", "start", "rollback removal", "rollback rename", "rollback restart"} {
		t.Run(stage, func(t *testing.T) {
			f := batchUpdateFixture(t, "demo", "next")
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "replacement injection", FailOnce: true}
			want := "restored sandbox demo to its previous state"
			switch stage {
			case "create":
				f.responses["create * --name sandboxed-agents.default.demo *"] = response{Exit: 125, Output: "create injection"}
			case "rollback removal":
				f.responses["rm --force sandboxed-agents.default.demo"] = response{Exit: 125, Output: "restore injection"}
				want = "rollback could not remove replacement"
			case "rollback rename":
				f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{Exit: 125, Output: "restore injection"}
				want = "rollback could not restore name"
			case "rollback restart":
				f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "restore injection"}
				want = "restored sandbox demo but could not start it"
			}
			status, output, calls := runUpExecutable(t, f, "update", "--all")
			if status != 1 || !strings.Contains(output, want) || !strings.Contains(output, "sandbox next updated") || !strings.Contains(output, "1 updated, 0 current, 0 volumes only, 1 not updated") {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			removed, next := -1, -1
			for i, call := range calls {
				joined := strings.Join(call, " ")
				if joined == "rm --force sandboxed-agents.default.demo" {
					removed = i
				}
				if joined == "rename sandboxed-agents.default.next sandboxed-agents-backup.default.next" {
					next = i
				}
				if call[0] == "volume" && call[1] != "exists" && call[1] != "inspect" && call[1] != "ls" {
					t.Fatalf("volume mutation: %v", call)
				}
			}
			if removed < 0 || next <= removed {
				t.Fatalf("next update did not follow rollback: %v", calls)
			}
		})
	}
}

// TestUpdateAllBuildsBeforeChangingSandboxes proves every required image succeeds before any replacement or session guard, and a failed build leaves every sandbox untouched through the executable CLI seam.
func TestUpdateAllBuildsBeforeChangingSandboxes(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			f := batchUpdateFixture(t, "anative", "bdotnet")
			for name, set := range map[string]string{"anative": "native", "bdotnet": "dotnet"} {
				key := "container inspect sandboxed-agents.default." + name
				r := f.responses[key]
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"`+set+`"`, 1)
				f.responses[key] = r
				f.responses["image exists "+strings.Replace(baseTag(t), ":none-", ":"+set+"-", 1)] = response{Exit: 1}
			}
			f.responses["build *"] = response{}
			if fail {
				f.responses["build * --tag "+strings.Replace(baseTag(t), ":none-", ":dotnet-", 1)+" *"] = response{Exit: 125, Output: "dotnet build failed"}
			}
			status, output, calls := runUpExecutable(t, f, "update", "--all")
			wantStatus := 0
			if fail {
				wantStatus = 1
			}
			if status != wantStatus {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			builds := 0
			for _, call := range calls {
				if call[0] == "build" {
					builds++
				}
				switch call[0] {
				case "rename", "create", "stop", "start", "rm", "exec":
					if fail || builds != 2 {
						t.Fatalf("sandbox touched before all builds succeeded: %v; builds=%d fail=%t output=%s", call, builds, fail, output)
					}
				}
			}
			if builds != 2 {
				t.Fatalf("builds=%d output=%s calls=%v", builds, output, calls)
			}
		})
	}
}

// TestUpdateAllPassesOverVolumesAndOwnerConflicts proves owned volumes-only sandboxes are successful skips while foreign containers, volumes and backups fail without image builds through the executable CLI seam.
func TestUpdateAllPassesOverVolumesAndOwnerConflicts(t *testing.T) {
	for _, state := range []string{"volumes only", "foreign container", "foreign volume", "foreign backup"} {
		t.Run(state, func(t *testing.T) {
			f := batchUpdateFixture(t, "demo", "next")
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"native"`, 1)
			wantStatus, want := 1, "owner conflict"
			switch state {
			case "volumes only":
				f.responses["ps --all --format json"] = response{Output: `[{"Names":["sandboxed-agents.default.next"]}]`}
				f.responses["volume ls --format json"] = response{Output: `[{"Name":"sandboxed-agents.default.demo.workspace"}]`}
				f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
				wantStatus, want = 0, "1 volumes only"
			case "foreign container":
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`, 1)
			case "foreign volume":
				f.responses["volume inspect sandboxed-agents.default.demo.home"] = response{Output: `[{"Labels":{}}]`}
			case "foreign backup":
				f.responses["container exists sandboxed-agents-backup.default.demo"] = response{}
				f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"foreign"}},"State":{"Running":false}}]`}
			}
			f.responses[key] = r
			status, output, calls := runUpExecutable(t, f, "update", "--all")
			if status != wantStatus || !strings.Contains(output, want) || !strings.Contains(output, "sandbox next updated") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			for _, call := range calls {
				joined := strings.Join(call, " ")
				if call[0] == "build" || strings.Contains(joined, ":native-") {
					t.Fatalf("image prepared for skipped sandbox: %v", call)
				}
				switch call[0] {
				case "rename", "create", "start", "stop", "rm":
					if strings.Contains(joined, ".demo") {
						t.Fatalf("skipped sandbox changed: %v", call)
					}
				}
			}
		})
	}
}

// TestUpdateAllGuardsSessionsPerSandbox proves session and manager refusals continue to other sandboxes while force reports ended sessions through the executable CLI seam.
func TestUpdateAllGuardsSessionsPerSandbox(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			name := "sessions"
			if unavailable {
				name = "manager unavailable"
			}
			if force {
				name += " forced"
			}
			t.Run(name, func(t *testing.T) {
				f := batchUpdateFixture(t, "demo", "next")
				answer := response{Output: "codex\nclaude\n"}
				if unavailable {
					answer = response{Exit: 125, Output: "manager unavailable"}
				}
				f.responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions"] = answer
				args := []string{"update", "--all"}
				wantStatus, want := 1, "running agent sessions: codex, claude"
				if unavailable {
					want = "cannot rule out running sessions"
				}
				if force {
					args = append(args, "--force")
					wantStatus, want = 0, "ended agent sessions: codex, claude"
					if unavailable {
						want = "sessions that may have been running were ended and cannot be named"
					}
				}
				status, output, calls := runUpExecutable(t, f, args...)
				if status != wantStatus || !strings.Contains(output, want) || !strings.Contains(output, "sandbox next updated") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				if !force {
					for _, call := range calls {
						switch call[0] {
						case "rename", "create", "start", "stop", "rm":
							if strings.Contains(strings.Join(call, " "), ".demo") {
								t.Fatalf("session sandbox changed without force: %v", call)
							}
						}
					}
				}
			})
		}
	}
}

// waitBatchFile waits up to five seconds for a file signaling progress at the batch-update
// process seam.
func waitBatchFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not reach %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUpdateAllSkipsLockedSandbox proves lock contention is reported before any sandbox inspection and other names still update through concurrent executable CLI processes.
func TestUpdateAllSkipsLockedSandbox(t *testing.T) {
	f := batchUpdateFixture(t, "demo", "next")
	ready := filepath.Join(t.TempDir(), "stopping")
	release := ready + ".release"
	f.responses["stop sandboxed-agents.default.demo"] = response{ReadyFile: ready, WaitFor: release}
	done := make(chan string, 1)
	go func() {
		status, output, _ := runUpExecutable(t, f, "stop", "demo")
		done <- fmt.Sprintf("%d:%s", status, output)
	}()
	defer os.WriteFile(release, nil, 0600)
	waitBatchFile(t, ready)
	status, output, calls := runUpExecutable(t, f, "update", "--all")
	if status != 1 || !strings.Contains(output, `sandbox "demo": another lifecycle`) || !strings.Contains(output, "retry") || !strings.Contains(output, "sandbox next updated") || !strings.Contains(output, "1 not updated") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	inventory := false
	for _, call := range calls {
		if call[0] == "ps" {
			inventory = true
		}
		if inventory && (call[0] == "build" || strings.Contains(strings.Join(call, " "), ".demo")) {
			t.Fatalf("locked sandbox inspected or built: %v", call)
		}
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
		t.Fatal("holding lifecycle command did not finish")
	}
}

// TestUpdateAllHoldsEveryLockThroughBuildAndRecovery proves even a current sandbox remains locked throughout builds, later replacements and recovery, with release at command exit through concurrent executable CLI processes.
func TestUpdateAllHoldsEveryLockThroughBuildAndRecovery(t *testing.T) {
	f := batchUpdateFixture(t, "acurrent", "breplace", "crecover")
	key := "container inspect sandboxed-agents.default.acurrent"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, "sha256:old", "sha256:current", 1)
	f.responses[key] = r
	key = "container inspect sandboxed-agents.default.breplace"
	r = f.responses[key]
	r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"native"`, 1)
	f.responses[key] = r
	f.responses["image exists "+strings.Replace(baseTag(t), ":none-", ":native-", 1)] = response{Exit: 1}
	key = "container inspect sandboxed-agents.default.crecover"
	r = f.responses[key]
	r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"default","io.sandboxed-agents.update-was-running":"true"`, 1)
	r.Output = strings.Replace(r.Output, `"Running":true`, `"Running":false`, 1)
	f.responses[key] = r
	f.responses["container exists sandboxed-agents-backup.default.crecover"] = response{}
	f.responses["container inspect sandboxed-agents-backup.default.crecover"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":false}}]`}
	buildReady := filepath.Join(t.TempDir(), "building")
	buildRelease := buildReady + ".release"
	recoverReady := filepath.Join(t.TempDir(), "recovering")
	recoverRelease := recoverReady + ".release"
	f.responses["build *"] = response{ReadyFile: buildReady, WaitFor: buildRelease}
	f.responses["start sandboxed-agents.default.crecover"] = response{ReadyFile: recoverReady, WaitFor: recoverRelease}
	f.responses["stop sandboxed-agents.default.acurrent"] = response{}
	done := make(chan string, 1)
	go func() {
		status, output, _ := runUpExecutable(t, f, "update", "--all")
		done <- fmt.Sprintf("%d:%s", status, output)
	}()
	defer os.WriteFile(buildRelease, nil, 0600)
	defer os.WriteFile(recoverRelease, nil, 0600)
	for _, stage := range []struct{ ready, release string }{{buildReady, buildRelease}, {recoverReady, recoverRelease}} {
		waitBatchFile(t, stage.ready)
		for _, name := range []string{"acurrent", "breplace", "crecover"} {
			status, output, _ := runUpExecutable(t, f, "stop", name)
			if status != 1 || !strings.Contains(output, "step 3:") || !strings.Contains(output, "retry") {
				t.Fatalf("competing stop %s=%d %s", name, status, output)
			}
		}
		if err := os.WriteFile(stage.release, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case result := <-done:
		if !strings.HasPrefix(result, "0:") || !strings.Contains(result, "sandbox crecover interrupted update cleaned up") {
			t.Fatal(result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("batch update did not finish")
	}
	status, output, _ := runUpExecutable(t, f, "stop", "acurrent")
	if status != 0 {
		t.Fatalf("lock retained after batch exit: %d %s", status, output)
	}
}

// TestUpdateAllUsesFreshInspectionAfterInventory proves vanished names fail and current running and owner facts override stale inventory records through the executable CLI seam.
func TestUpdateAllUsesFreshInspectionAfterInventory(t *testing.T) {
	for _, state := range []string{"gone", "foreign", "running", "current"} {
		t.Run(state, func(t *testing.T) {
			f := batchUpdateFixture(t, "demo", "next")
			f.responses["ps --all --format json"] = response{Output: `[{"Names":["sandboxed-agents.default.demo"],"State":"exited","Image":"sha256:stale","Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}},{"Names":["sandboxed-agents.default.next"]},{"Names":["sandboxed-agents.other.foreign"]}]`}
			f.responses["volume ls --format json"] = response{Output: `[{"Name":"sandboxed-agents.other.foreign.workspace"}]`}
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			wantStatus, want := 1, `sandbox "demo" does not exist in this controller group`
			switch state {
			case "gone":
				f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
				f.responses["volume exists sandboxed-agents.default.demo.*"] = response{Exit: 1}
			case "foreign":
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`, 1)
				want = "owner conflict"
			case "running":
				f.responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions"] = response{Output: "codex\n"}
				want = "running agent sessions: codex"
			case "current":
				r.Output = strings.Replace(r.Output, "sha256:old", "sha256:current", 1)
				wantStatus, want = 0, "sandbox demo already up to date"
			}
			f.responses[key] = r
			status, output, calls := runUpExecutable(t, f, "update", "--all")
			if status != wantStatus || !strings.Contains(output, want) || !strings.Contains(output, "sandbox next updated") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			inventories := 0
			for _, call := range calls {
				joined := strings.Join(call, " ")
				if joined == "ps --all --format json" || joined == "volume ls --format json" {
					inventories++
				}
				if strings.Contains(joined, ".other.") || strings.Contains(joined, ":azure-") {
					t.Fatalf("acted on foreign group or stale inventory: %v", call)
				}
				switch call[0] {
				case "rename", "create", "start", "stop", "rm":
					if strings.Contains(joined, ".demo") {
						t.Fatalf("sandbox changed despite fresh refusal or current image: %v", call)
					}
				}
			}
			if inventories != 2 {
				t.Fatalf("inventory calls=%d; want one container and one volume inventory", inventories)
			}
		})
	}
}

// TestUpdateAllRecoversBackupsAndContinues proves backup-only names are discovered and recovery success or restoration never builds their image or prevents another update through the executable CLI seam.
func TestUpdateAllRecoversBackupsAndContinues(t *testing.T) {
	for _, backupOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready replacement", true: "backup only"}[backupOnly], func(t *testing.T) {
			f := batchUpdateFixture(t, "demo", "next")
			f.responses["ps --all --format json"] = response{Output: `[{"Names":["sandboxed-agents-backup.default.demo"]},{"Names":["sandboxed-agents.default.next"]}]`}
			f.responses["container exists sandboxed-agents-backup.default.demo"] = response{}
			f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":false}}]`}
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"native","io.sandboxed-agents.update-was-running":"true"`, 1)
			f.responses[key] = r
			wantStatus, want := 0, "sandbox demo interrupted update cleaned up"
			if backupOnly {
				f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
				f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
				wantStatus, want = 1, "previous state could not be determined, sandbox stays stopped; use start demo"
			}
			status, output, calls := runUpExecutable(t, f, "update", "--all")
			if status != wantStatus || !strings.Contains(output, want) || !strings.Contains(output, "sandbox next updated") {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			for _, call := range calls {
				joined := strings.Join(call, " ")
				if call[0] == "build" || strings.Contains(joined, ":native-") || (call[0] == "create" && strings.Contains(joined, ".demo")) {
					t.Fatalf("rebuilt interrupted sandbox: %v", call)
				}
			}
		})
	}
}

// TestUpdateAllKeepsWindowsTarget proves inventory, builds, replacements and rollback remain bound to the machine selected once by preflight through the executable CLI seam.
func TestUpdateAllKeepsWindowsTarget(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[rollback], func(t *testing.T) {
			f := batchUpdateFixture(t, "demo", "next")
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"native"`, 1)
			f.responses[key] = r
			f.responses["image exists "+strings.Replace(baseTag(t), ":none-", ":native-", 1)] = response{Exit: 1}
			f.responses["build *"] = response{}
			if rollback {
				f.responses["start sandboxed-agents.default.next"] = response{Exit: 125, FailOnce: true, Output: "start failed"}
				f.responses["rm --force sandboxed-agents.default.next"] = response{}
				f.responses["rename sandboxed-agents-backup.default.next sandboxed-agents.default.next"] = response{}
			}
			win := windowsHost(t)
			for key, value := range f.responses {
				if key != "--version" && key != "info --format json" {
					win.responses["--connection chosen "+key] = value
				}
			}
			t.Setenv("TEST_SSH_OUTPUT", "debug1: SSH2_MSG_NEWKEYS received\n")
			t.Setenv("TEST_SSH_EXIT", "255")
			t.Setenv("TEST_SSH_KNOWN_HOSTS", "[127.0.0.1]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n")
			status, output, calls := runUpExecutable(t, win, "update", "--all")
			wantStatus := 0
			if rollback {
				wantStatus = 1
				if !strings.Contains(output, "restored sandbox next to its previous state") {
					t.Fatalf("missing rollback: %s", output)
				}
			}
			if status != wantStatus || !strings.Contains(output, "sandbox demo updated") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			selections := 0
			for _, call := range calls {
				if strings.Join(call, " ") == "machine list --format json" {
					selections++
				}
				if call[0] != "machine" && call[0] != "--version" && (len(call) < 3 || call[0] != "--connection" || call[1] != "chosen") {
					t.Fatalf("unbound target: %v", call)
				}
			}
			if selections != 1 {
				t.Fatalf("machine selections=%d", selections)
			}
		})
	}
}

// TestUpdateAllRejectsInvalidUsage proves absent or conflicting targets and toolchain overrides fail before a failing preflight can call Podman through the executable CLI seam.
func TestUpdateAllRejectsInvalidUsage(t *testing.T) {
	for _, args := range [][]string{{"update"}, {"update", "demo", "--all"}, {"update", "--all", "--with", "native"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := linuxHost(t)
			f.responses["--version"] = response{Exit: 125, Output: "preflight would fail"}
			status, output, calls := runUpExecutable(t, f, args...)
			if status != 2 || !strings.Contains(output, "usage:") || len(calls) != 0 {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
		})
	}
}
