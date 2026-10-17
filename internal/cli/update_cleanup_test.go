package cli_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// cleanupFixture scripts a replacement and retained backup with the requested prior and current
// running states.
func cleanupFixture(t *testing.T, wasRunning, running bool) *fixture {
	t.Helper()
	f := replacementFixture(t)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	value := "false"
	if wasRunning {
		value = "true"
	}
	r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"default","io.sandboxed-agents.update-was-running":"`+value+`"`, 1)
	if !running {
		r.Output = strings.Replace(r.Output, `"Running":true`, `"Running":false`, 1)
	}
	f.responses[key] = r
	f.responses["container exists sandboxed-agents-backup.default.demo"] = response{}
	f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":false}}]`}
	f.responses["stop sandboxed-agents.default.demo"] = response{}
	return f
}

// TestUpdateCleansReadyReplacementBackup proves a retry checks readiness and removes only the superseded backup without image work through the executable CLI and fake Podman seam.
func TestUpdateCleansReadyReplacementBackup(t *testing.T) {
	f := cleanupFixture(t, true, true)
	status, output, calls := runUpExecutable(t, f, "update", "demo", "--with", "native")
	if status != 0 || !strings.Contains(output, "interrupted update cleaned up") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	ready, removed := -1, -1
	for i, call := range calls {
		joined := strings.Join(call, " ")
		if strings.HasSuffix(joined, "sandboxed-agents-manager version") {
			ready = i
		}
		if joined == "rm sandboxed-agents-backup.default.demo" {
			removed = i
			continue
		}
		switch call[0] {
		case "image", "build", "create", "rename", "rm", "start", "stop":
			t.Fatalf("unexpected cleanup action: %v", call)
		case "volume":
			if call[1] != "exists" && call[1] != "inspect" {
				t.Fatalf("volume changed: %v", call)
			}
		}
	}
	if ready < 0 || removed <= ready {
		t.Fatalf("readiness must precede backup removal: %v", calls)
	}
}

// TestUpdateCleanupRestoresRecordedState proves all current/prior running combinations start only a stopped replacement and stop it only after backup removal through the executable CLI seam.
func TestUpdateCleanupRestoresRecordedState(t *testing.T) {
	for _, previous := range []bool{false, true} {
		for _, current := range []bool{false, true} {
			t.Run(fmt.Sprintf("previous=%t/current=%t", previous, current), func(t *testing.T) {
				f := cleanupFixture(t, previous, current)
				status, output, calls := runUpExecutable(t, f, "update", "demo")
				if status != 0 {
					t.Fatalf("status=%d output=%s", status, output)
				}
				got := []string{}
				for _, call := range calls {
					if call[0] == "exec" {
						got = append(got, "ready")
						continue
					}
					switch call[0] {
					case "start", "stop", "rm":
						got = append(got, strings.Join(call, " "))
					case "build", "image", "create", "rename":
						t.Fatalf("unexpected cleanup work: %v", call)
					}
				}
				want := []string{}
				if !current {
					want = append(want, "start sandboxed-agents.default.demo")
				}
				want = append(want, "ready", "rm sandboxed-agents-backup.default.demo")
				if !previous {
					want = append(want, "stop sandboxed-agents.default.demo")
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("actions=%v want=%v", got, want)
				}
			})
		}
	}
}

// TestUpdateCleanupRetainsUnsafeBackup proves missing running state and owner conflicts prevent readiness, builds and mutations through CLI inspection fixtures.
func TestUpdateCleanupRetainsUnsafeBackup(t *testing.T) {
	for _, state := range []string{"missing backup state", "foreign replacement", "foreign backup", "foreign volume"} {
		t.Run(state, func(t *testing.T) {
			f := cleanupFixture(t, true, false)
			key := "container inspect sandboxed-agents.default.demo"
			old, replacement := "", ""
			switch state {
			case "missing backup state":
				f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
			case "foreign replacement":
				old, replacement = `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`
			case "foreign backup":
				key = "container inspect sandboxed-agents-backup.default.demo"
				old, replacement = `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"foreign"`
			case "foreign volume":
				f.responses["volume inspect *"] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"foreign"}}]`}
			}
			if old != "" {
				r := f.responses[key]
				r.Output = strings.Replace(r.Output, old, replacement, 1)
				f.responses[key] = r
			}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			want := "backup retained"
			if strings.HasPrefix(state, "foreign") {
				want = "owner conflict"
			}
			if status != 1 || !strings.Contains(output, want) {
				t.Fatalf("status=%d output=%s", status, output)
			}
			for _, call := range calls {
				switch call[0] {
				case "start", "stop", "rm", "exec", "build", "image", "create", "rename":
					t.Fatalf("unsafe cleanup action: %v", call)
				case "volume":
					if call[1] != "exists" && call[1] != "inspect" {
						t.Fatalf("volume mutation: %v", call)
					}
				}
			}
		})
	}
}

// TestUpdateCleanupFailuresRemainRetryable proves backup removal failures retain the backup and final-stop failures report a committed update through the executable CLI seam.
func TestUpdateCleanupFailuresRemainRetryable(t *testing.T) {
	for _, stage := range []string{"rm", "stop"} {
		t.Run(stage, func(t *testing.T) {
			f := cleanupFixture(t, false, false)
			failed := stage + " sandboxed-agents.default.demo"
			if stage == "rm" {
				failed = "rm sandboxed-agents-backup.default.demo"
			}
			f.responses[failed] = response{Exit: 125, Output: "cleanup injection"}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "cleanup injection") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if strings.Join(calls[len(calls)-1], " ") != failed {
				t.Fatalf("unsafe action after failure: %v", calls)
			}
			if stage == "rm" && (!strings.Contains(output, "update demo cleans up") || !strings.Contains(output, "still running")) {
				t.Fatalf("missing retry guidance: %s", output)
			}
			if stage == "stop" {
				if !strings.Contains(output, "update succeeded but sandbox is still running") || !strings.Contains(output, "use stop demo") {
					t.Fatalf("missing committed update warning: %s", output)
				}
				return
			}
			// Model the persisted Podman state on the next invocation after the transient failure.
			f.responses[failed] = response{}
			if stage == "rm" {
				key := "container inspect sandboxed-agents.default.demo"
				r := f.responses[key]
				r.Output = strings.Replace(r.Output, `"Running":false`, `"Running":true`, 1)
				f.responses[key] = r
			}
			status, output, calls = runUpExecutable(t, f, "update", "demo")
			if status != 0 || !strings.Contains(output, "interrupted update cleaned up") {
				t.Fatalf("retry=%d %s", status, output)
			}
			if strings.Join(calls[len(calls)-2], " ") != "rm sandboxed-agents-backup.default.demo" || strings.Join(calls[len(calls)-1], " ") != "stop sandboxed-agents.default.demo" {
				t.Fatalf("retry did not restore state: %v", calls)
			}
		})
	}
}

// TestUpdateCleanupReadinessFailureRestoresBackup proves an unusable replacement is removed and the previously stopped sandbox is restored without volume mutation through the executable CLI and failed SSH probe seam.
func TestUpdateCleanupReadinessFailureRestoresBackup(t *testing.T) {
	f := cleanupFixture(t, false, false)
	f.responses["rm --force sandboxed-agents.default.demo"] = response{}
	f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
	t.Setenv("TEST_SSH_KNOWN_HOSTS", "invalid\n")
	status, output, calls := runUpExecutableWithShortReadiness(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "readiness wait failed") || !strings.Contains(output, "restored sandbox demo to its previous state") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	want := []string{"start sandboxed-agents.default.demo", "rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
	if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions=%v want=%v", got, want)
	}
}

// TestUpdateRecoveryRestoresAfterWrongOwnedVolume proves recovery restores the old sandbox without starting or probing a replacement attached to another sandbox's home volume through the executable CLI seam.
func TestUpdateRecoveryRestoresAfterWrongOwnedVolume(t *testing.T) {
	f := cleanupFixture(t, false, false)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, `"Name":"sandboxed-agents.default.demo.home"`, `"Name":"sandboxed-agents.default.other.home"`, 1)
	f.responses[key] = r
	// All volume names, including the other sandbox's home, exist and have this group's owner.
	f.responses["volume exists sandboxed-agents.default.other.home"] = response{}
	f.responses["volume inspect sandboxed-agents.default.other.home"] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	f.responses["rm --force sandboxed-agents.default.demo"] = response{}
	f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "replacement storage") || !strings.Contains(output, "restored sandbox demo") || strings.Contains(output, "owner conflict") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	if ssh := testutil.Invocations(t, f.fakes.SSHLog); len(ssh) != 0 {
		t.Fatalf("SSH readiness attempted on wrong storage: %v", ssh)
	}
	want := []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
	if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions=%v want=%v", got, want)
	}
}

// TestUpdateRecoveryRestoresAfterMalformedStorage proves wrong, missing, duplicate and extra mounts restore the old stopped sandbox without readiness probes or volume changes through the executable CLI seam.
func TestUpdateRecoveryRestoresAfterMalformedStorage(t *testing.T) {
	workspace := `{"Type":"volume","Name":"sandboxed-agents.default.demo.workspace","Destination":"/workspace"}`
	home := `{"Type":"volume","Name":"sandboxed-agents.default.demo.home","Destination":"/home/agent"}`
	ssh := `{"Type":"volume","Name":"sandboxed-agents.default.demo.ssh","Destination":"/etc/ssh"}`
	type storageCase struct{ name, from, to, absentVolume string }
	cases := []storageCase{}
	for _, mount := range []struct{ name, record string }{{"workspace", workspace}, {"home", home}, {"ssh", ssh}} {
		cases = append(cases,
			storageCase{"wrong " + mount.name, mount.record, strings.Replace(mount.record, ".default.demo.", ".default.other.", 1), ""},
			storageCase{"missing volume " + mount.name, "", "", "sandboxed-agents.default.demo." + mount.name},
			storageCase{"missing mount " + mount.name, mount.record, "", ""},
			storageCase{"duplicate " + mount.name, mount.record, mount.record + "," + mount.record, ""},
		)
	}
	cases = append(cases,
		storageCase{"extra mount", ssh, ssh + `,{"Type":"volume","Name":"extra","Destination":"/extra"}`, ""},
		storageCase{"home bind", home, `{"Type":"bind","Source":"/somewhere","Destination":"/home/agent"}`, ""},
		storageCase{"SSH bind", ssh, `{"Type":"bind","Source":"/somewhere","Destination":"/etc/ssh"}`, ""},
		storageCase{"empty workspace bind", workspace, `{"Type":"bind","Source":"","Destination":"/workspace"}`, ""},
	)
	for _, current := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("running=%t/%s", current, tc.name), func(t *testing.T) {
				f := cleanupFixture(t, false, current)
				key := "container inspect sandboxed-agents.default.demo"
				r := f.responses[key]
				if tc.from != "" {
					from := tc.from
					if tc.to == "" {
						if from == ssh {
							from = "," + from
						} else {
							from += ","
						}
					}
					if strings.Count(r.Output, from) != 1 {
						t.Fatal("mount fixture no longer matches")
					}
					r.Output = strings.Replace(r.Output, from, tc.to, 1)
					f.responses[key] = r
				}
				if tc.absentVolume != "" {
					f.responses["volume exists "+tc.absentVolume] = response{Exit: 1}
				}
				f.responses["rm --force sandboxed-agents.default.demo"] = response{}
				f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
				status, output, calls := runUpExecutable(t, f, "update", "demo")
				if status != 1 || !strings.Contains(output, "replacement storage") || !strings.Contains(output, "restored sandbox demo") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				if ssh := testutil.Invocations(t, f.fakes.SSHLog); len(ssh) != 0 {
					t.Fatalf("SSH readiness attempted on malformed storage: %v", ssh)
				}
				want := []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
				if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
					t.Fatalf("actions=%v want=%v", got, want)
				}
			})
		}
	}
}

// TestUpdateCleanupAcceptsWorkspaceBind proves cleanup accepts a supported workspace bind without a workspace volume and without reconstructing images, toolchains or resource limits through the executable CLI seam.
func TestUpdateCleanupAcceptsWorkspaceBind(t *testing.T) {
	f := cleanupFixture(t, false, false)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, `"Type":"volume","Name":"sandboxed-agents.default.demo.workspace"`, `"Type":"bind","Source":"/projects/my work"`, 1)
	r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.toolchains":"none"`, `"io.sandboxed-agents.toolchains":"unknown"`, 1)
	r.Output = strings.Replace(r.Output, `"Memory":8589934592`, `"Memory":null`, 1)
	f.responses[key] = r
	f.responses["volume exists sandboxed-agents.default.demo.workspace"] = response{Exit: 1}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 0 || !strings.Contains(output, "interrupted update cleaned up") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		switch call[0] {
		case "create", "rename", "build", "image":
			t.Fatalf("unexpected reconstruction: %v", call)
		case "volume":
			if call[1] != "exists" && call[1] != "inspect" {
				t.Fatalf("volume mutation: %v", call)
			}
		}
	}
	last := flatCalls(calls[len(calls)-2:])
	if strings.Join(last, ";") != "rm sandboxed-agents-backup.default.demo;stop sandboxed-agents.default.demo" {
		t.Fatalf("wrong finalization order: %v", calls)
	}
}

// TestUpdateCleanupOwnerConflictPrecedesStorageValidation proves a foreign backup is reported before a replacement's invalid storage with no mutation or readiness probe through the executable CLI seam.
func TestUpdateCleanupOwnerConflictPrecedesStorageValidation(t *testing.T) {
	f := cleanupFixture(t, false, false)
	key := "container inspect sandboxed-agents.default.demo"
	r := f.responses[key]
	r.Output = strings.Replace(r.Output, `"Name":"sandboxed-agents.default.demo.home"`, `"Name":"sandboxed-agents.default.other.home"`, 1)
	f.responses[key] = r
	f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"foreign"}},"State":{"Running":false}}]`}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, "sandboxed-agents-backup.default.demo") || strings.Contains(output, "replacement storage") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	for _, call := range calls {
		switch call[0] {
		case "start", "stop", "rm", "exec", "create", "rename", "build", "image":
			t.Fatalf("action on foreign sandbox state: %v", call)
		}
	}
}
