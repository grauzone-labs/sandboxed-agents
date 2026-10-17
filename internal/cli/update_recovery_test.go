package cli_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestUpdateRecoveryRestoresUnusableReplacement proves missing running state, an invalid port, or unsupported storage restores the old sandbox without probing or starting the replacement through the executable CLI seam.
func TestUpdateRecoveryRestoresUnusableReplacement(t *testing.T) {
	for _, state := range []string{"missing running state", "invalid port", "unsupported storage"} {
		t.Run(state, func(t *testing.T) {
			f := cleanupFixture(t, true, false)
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			switch state {
			case "missing running state":
				r.Output = strings.Replace(r.Output, `"Running":false`, `"Running":null`, 1)
			case "invalid port":
				r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.port":"2222"`, `"io.sandboxed-agents.port":"bad"`, 1)
			case "unsupported storage":
				r.Output = strings.Replace(r.Output, `"Name":"sandboxed-agents.default.demo.home"`, `"Name":"sandboxed-agents.default.other.home"`, 1)
			}
			f.responses[key] = r
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "restored sandbox demo to its previous state") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			want := []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo", "start sandboxed-agents.default.demo"}
			if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("actions=%v want=%v", got, want)
			}
			for _, call := range calls {
				if call[0] == "exec" {
					t.Fatalf("probed unusable replacement: %v", call)
				}
			}
		})
	}
}

// TestUpdateRecoversWithoutReplacement proves backup-only interruptions restore the running sandbox or leave an indeterminate sandbox stopped with start guidance through the executable CLI seam.
func TestUpdateRecoversWithoutReplacement(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("backup running=%t", running), func(t *testing.T) {
			f := cleanupFixture(t, true, false)
			f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
			f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: fmt.Sprintf(`[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":%t}}]`, running)}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "restored sandbox demo") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if !running && (!strings.Contains(output, "previous state could not be determined") || !strings.Contains(output, "stays stopped") || !strings.Contains(output, "start demo")) {
				t.Fatalf("missing indeterminate state guidance: %s", output)
			}
			want := []string{"rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
			if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("actions=%v want=%v", got, want)
			}
			// The next invocation sees the restored old sandbox and performs the update.
			f = replacementFixture(t)
			if !running {
				key := "container inspect sandboxed-agents.default.demo"
				r := f.responses[key]
				r.Output = strings.Replace(r.Output, `"Running":true`, `"Running":false`, 1)
				f.responses[key] = r
				f.responses["stop sandboxed-agents.default.demo"] = response{}
			}
			status, output, _ = runUpExecutable(t, f, "update", "demo")
			if status != 0 || !strings.Contains(output, "sandbox demo updated") {
				t.Fatalf("retry=%d %s", status, output)
			}
		})
	}
}

// TestUpdateRecoversAfterReplacementStartFailure proves a replacement that cannot start is removed before restoring and restarting the old sandbox through the executable CLI seam.
func TestUpdateRecoversAfterReplacementStartFailure(t *testing.T) {
	f := cleanupFixture(t, true, false)
	f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "replacement cannot start", FailOnce: true}
	f.responses["rm --force sandboxed-agents.default.demo"] = response{}
	f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "replacement cannot start") || !strings.Contains(output, "restored sandbox demo to its previous state") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	want := []string{"start sandboxed-agents.default.demo", "rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo", "start sandboxed-agents.default.demo"}
	if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions=%v want=%v", got, want)
	}
}

// TestUpdateRecoveryUnknownPreviousStateStaysStopped proves missing or invalid previous-state labels restore the old sandbox stopped and name start NAME through the executable CLI seam.
func TestUpdateRecoveryUnknownPreviousStateStaysStopped(t *testing.T) {
	for _, label := range []string{"", "invalid"} {
		t.Run(label, func(t *testing.T) {
			f := cleanupFixture(t, true, true)
			key := "container inspect sandboxed-agents.default.demo"
			r := f.responses[key]
			r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.update-was-running":"true"`, `"io.sandboxed-agents.update-was-running":"`+label+`"`, 1)
			f.responses[key] = r
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "previous state could not be determined") || !strings.Contains(output, "stays stopped") || !strings.Contains(output, "start demo") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			want := []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
			if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("actions=%v want=%v", got, want)
			}
		})
	}
}

// TestUpdateRecoveryRefusesOwnerConflicts proves missing and foreign owners of either container or any sandbox volume prevent all recovery actions through the executable CLI seam.
func TestUpdateRecoveryRefusesOwnerConflicts(t *testing.T) {
	for _, object := range []string{"container sandboxed-agents.default.demo", "container sandboxed-agents-backup.default.demo", "volume sandboxed-agents.default.demo.workspace", "volume sandboxed-agents.default.demo.home", "volume sandboxed-agents.default.demo.ssh"} {
		for _, owner := range []string{"", "foreign"} {
			t.Run(object+"/owner="+owner, func(t *testing.T) {
				f := cleanupFixture(t, true, true)
				kind, name, _ := strings.Cut(object, " ")
				key := kind + " inspect " + name
				if kind == "volume" {
					f.responses[key] = response{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				} else {
					r := f.responses[key]
					r.Output = strings.Replace(r.Output, `"io.sandboxed-agents.owner":"default"`, `"io.sandboxed-agents.owner":"`+owner+`"`, 1)
					f.responses[key] = r
				}
				status, output, calls := runUpExecutable(t, f, "update", "demo")
				if status != 1 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, name) || !strings.Contains(output, "Podman") {
					t.Fatalf("status=%d output=%s", status, output)
				}
				if got := recoveryActions(t, calls); len(got) != 0 {
					t.Fatalf("actions on owner conflict: %v", got)
				}
				for _, call := range calls {
					if call[0] == "exec" {
						t.Fatalf("readiness on owner conflict: %v", call)
					}
				}
			})
		}
	}
}

// TestUpdateRecoveryStopsAfterRestoreFailure proves failed removal, rename, or restart retains recoverable state without later mutations through the executable CLI seam.
func TestUpdateRecoveryStopsAfterRestoreFailure(t *testing.T) {
	for _, failed := range []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo", "start sandboxed-agents.default.demo"} {
		t.Run(failed, func(t *testing.T) {
			f := cleanupFixture(t, true, false)
			f.responses["start sandboxed-agents.default.demo"] = response{Exit: 125, Output: "start failed", FailOnce: true}
			f.responses["rm --force sandboxed-agents.default.demo"] = response{}
			f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
			f.responses[failed] = response{Exit: 125, Output: "restore injection"}
			status, output, calls := runUpExecutable(t, f, "update", "demo")
			if status != 1 || !strings.Contains(output, "restore injection") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			actions := recoveryActions(t, calls)
			if len(actions) == 0 || actions[len(actions)-1] != failed {
				t.Fatalf("continued after failed action: %v", actions)
			}
			if failed == "start sandboxed-agents.default.demo" {
				if !strings.Contains(output, "restored sandbox demo but could not start it") || !strings.Contains(output, "use start demo") {
					t.Fatalf("missing restored-state guidance: %s", output)
				}
			} else if !strings.Contains(output, "backup retained") {
				t.Fatalf("missing retained-backup guidance: %s", output)
			}
		})
	}
}

// TestUpdateRecoversRunningBackup proves an interruption before stopping the old sandbox restores its name without starting the replacement or building through the executable CLI seam.
func TestUpdateRecoversRunningBackup(t *testing.T) {
	f := cleanupFixture(t, true, false)
	f.responses["container inspect sandboxed-agents-backup.default.demo"] = response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":true}}]`}
	f.responses["rm --force sandboxed-agents.default.demo"] = response{}
	f.responses["rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"] = response{}
	status, output, calls := runUpExecutable(t, f, "update", "demo")
	if status != 1 || !strings.Contains(output, "restored sandbox demo to its previous state") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	want := []string{"rm --force sandboxed-agents.default.demo", "rename sandboxed-agents-backup.default.demo sandboxed-agents.default.demo"}
	if got := recoveryActions(t, calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("actions=%v want=%v", got, want)
	}
}

// recoveryActions extracts recovery lifecycle actions while rejecting image work and volume
// mutations.
func recoveryActions(t *testing.T, calls [][]string) []string {
	t.Helper()
	var actions []string
	for _, call := range calls {
		switch call[0] {
		case "image", "build", "create":
			t.Fatalf("image work during recovery: %v", call)
		case "volume":
			if call[1] != "exists" && call[1] != "inspect" {
				t.Fatalf("volume mutation: %v", call)
			}
		case "rm", "rename", "start", "stop":
			actions = append(actions, strings.Join(call, " "))
		}
	}
	return actions
}
