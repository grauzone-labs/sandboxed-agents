package liverun_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// preparedStatusRun prepares an isolated run through the command seam and returns its service, a status invoker and the run directory.
func preparedStatusRun(t *testing.T) (*liverun.Service, func(...string) (string, error), string) {
	t.Helper()
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatalf("prepare: %v %s", err, &out)
	}
	status := func(extra ...string) (string, error) {
		var out bytes.Buffer
		err := svc.RunDispatch(context.Background(), append([]string{"status", "-run-dir", run}, extra...), &out, &out)
		return out.String(), err
	}
	return svc, status, run
}

// TestStatusRejectsNullCompletionFields proves status refuses an exit record whose
// required Code or Interrupted field is JSON null instead of reporting a
// successful exit. It exercises the status command and exit artifact seam.
func TestStatusRejectsNullCompletionFields(t *testing.T) {
	for _, field := range []string{"Code", "Interrupted"} {
		t.Run(field, func(t *testing.T) {
			_, status, run := preparedStatusRun(t)
			values := map[string]string{"Code": "7", "Interrupted": "false"}
			values[field] = "null"
			record := `{"Code":` + values["Code"] + `,"Interrupted":` + values["Interrupted"] + `,"EndedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
			if err := os.WriteFile(filepath.Join(run, "exit.json"), []byte(record), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := status()
			if err == nil || strings.Contains(out, `"exit_code":0`) {
				t.Fatalf("null %s accepted as completion: %v %s", field, err, out)
			}
			if _, err := os.Stat(filepath.Join(run, "exit-status")); !os.IsNotExist(err) {
				t.Fatalf("null %s published a shell status: %v", field, err)
			}
		})
	}
}

// deadOwnership records a supervisor and suite child that both provably ended, so status may recover the run.
func deadOwnership(t *testing.T, run string) {
	t.Helper()
	dead := map[string]any{"PID": 2147483647, "Identity": "dead"}
	cleanupArtifact(t, run, "processes.json", map[string]any{"RunDir": run, "Supervisor": dead, "Child": dead})
}

// TestStatusRecoversStartupFromLaunchOwnership proves status reads the
// launcher's supervisor record before startup evidence exists: a live startup
// stays running false and not ended, a verified dead supervisor that never
// opened the suite log ends as interrupted, and a reservation without a
// supervisor identity stays unknown. It exercises the status command and the
// launch ownership artifact seam.
func TestStatusRecoversStartupFromLaunchOwnership(t *testing.T) {
	identity, err := liverun.OwnedProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		supervisor map[string]any
		ended      bool
	}{
		{"live", map[string]any{"PID": os.Getpid(), "Identity": identity}, false},
		{"dead", map[string]any{"PID": 2147483647, "Identity": "dead"}, true},
		{"reservation", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, status, run := preparedStatusRun(t)
			record := map[string]any{"RunDir": run}
			if tc.supervisor != nil {
				record["Supervisor"] = tc.supervisor
			}
			cleanupArtifact(t, run, "launching.json", record)
			out, err := status()
			if err != nil || !strings.Contains(out, `"running":false`) || !strings.Contains(out, fmt.Sprintf(`"ended":%t`, tc.ended)) {
				t.Fatalf("startup state misreported: %v %s", err, out)
			}
			if tc.ended && (!strings.Contains(out, `"exit_code":137`) || !strings.Contains(out, `"interrupted":true`)) {
				t.Fatalf("dead startup not interrupted: %s", out)
			}
		})
	}
}

// TestStatusKeepsUnknownChildOwnershipUnverified proves status synthesizes no
// interrupted exit from launching.json alone once the suite may have started,
// unless the platform ends the suite with its supervisor (the Windows job).
// It exercises the status command and the process ownership artifact seam.
func TestStatusKeepsUnknownChildOwnershipUnverified(t *testing.T) {
	_, status, run := preparedStatusRun(t)
	cleanupArtifact(t, run, "started.json", map[string]any{"StartedAt": time.Now().UTC()})
	cleanupArtifact(t, run, "launching.json", map[string]any{"RunDir": run, "Supervisor": map[string]any{"PID": 2147483647, "Identity": "dead"}})
	if err := os.WriteFile(filepath.Join(run, "suite.log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := status()
	ended := runtime.GOOS == "windows"
	if err != nil || !strings.Contains(out, fmt.Sprintf(`"ended":%t`, ended)) {
		t.Fatalf("unknown suite ownership reported as ended=%t: %v %s", !ended, err, out)
	}
	if _, err := os.Stat(filepath.Join(run, "exit.json")); ended == os.IsNotExist(err) {
		t.Fatalf("exit evidence does not match ownership: %v", err)
	}
}

// TestStatusPublishesExitRecordBeforeShellStatus proves a failed exit.json
// publication leaves the shell's exit-status untouched, so a shell caller never
// reads a code that the authoritative record does not hold. It exercises the
// status command's interrupted-recovery and exit artifact seam.
func TestStatusPublishesExitRecordBeforeShellStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions cannot block file creation on Windows")
	}
	_, status, run := preparedStatusRun(t)
	if _, err := status(); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "started.json", map[string]any{"StartedAt": time.Now().UTC()})
	deadOwnership(t, run)
	if err := os.WriteFile(filepath.Join(run, "exit-status"), []byte("stale\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A read-only directory refuses the exit record's temporary file while the existing shell status stays writable.
	if err := os.Chmod(run, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(run, 0700) })
	// A process that bypasses directory permissions, such as one with CAP_DAC_OVERRIDE, cannot see the fault.
	if probe, err := os.CreateTemp(run, ".permission-probe-"); err == nil {
		probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("directory permissions do not block file creation for this process")
	}
	if out, err := status(); err == nil {
		t.Fatalf("unpublished exit record reported: %s", out)
	}
	if got := string(mustReadFile(t, filepath.Join(run, "exit-status"))); got != "stale\n" {
		t.Fatalf("shell status written before exit record: %q", got)
	}
}

// TestStatusReportsRetainedExitWhenFilesystemProbeFails proves a finished run
// still reports its retained suite exit, with headroom marked unavailable, when
// the fresh free-space probe fails, and that a running run keeps refusing. It
// exercises the status command and the injected filesystem boundary seam.
func TestStatusReportsRetainedExitWhenFilesystemProbeFails(t *testing.T) {
	svc, status, run := preparedStatusRun(t)
	svc.FreeSpace = func(string) (uint64, error) { return 0, fmt.Errorf("engine unavailable") }
	if out, err := status(); err == nil || !strings.Contains(err.Error(), "filesystem") {
		t.Fatalf("unfinished run hid the failed probe: %v %s", err, out)
	}
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 7, "Interrupted": false, "EndedAt": time.Now().UTC()})
	out, err := status()
	if err != nil || !strings.Contains(out, `"exit_code":7`) || !strings.Contains(out, `"free_space_verified":false`) || !strings.Contains(out, `"free_space":[]`) {
		t.Fatalf("retained exit lost on probe failure: %v %s", err, out)
	}
	svc.FreeSpace = func(string) (uint64, error) { return 900, nil }
	out, err = status()
	if err != nil || !strings.Contains(out, `"free_bytes":900`) || strings.Contains(out, "free_space_verified") {
		t.Fatalf("free-space fields changed for a working probe: %v %s", err, out)
	}
}
