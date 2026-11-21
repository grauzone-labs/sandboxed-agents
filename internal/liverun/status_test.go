package liverun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// preparedStatusRun prepares an isolated run through the command seam and returns a status invoker and the run directory.
func preparedStatusRun(t *testing.T) (func(...string) (string, error), string) {
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
	return status, run
}

// TestStatusRejectsNullCompletionFields proves status refuses an exit record whose
// required Code or Interrupted field is JSON null instead of reporting a
// successful exit. It exercises the status command and exit artifact seam.
func TestStatusRejectsNullCompletionFields(t *testing.T) {
	for _, field := range []string{"Code", "Interrupted"} {
		t.Run(field, func(t *testing.T) {
			status, run := preparedStatusRun(t)
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

// TestStatusPublishesExitRecordBeforeShellStatus proves a failed exit.json
// publication leaves the shell's exit-status untouched, so a shell caller never
// reads a code that the authoritative record does not hold. It exercises the
// status command's interrupted-recovery and exit artifact seam.
func TestStatusPublishesExitRecordBeforeShellStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions cannot block file creation on Windows")
	}
	status, run := preparedStatusRun(t)
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
	if out, err := status(); err == nil {
		t.Fatalf("unpublished exit record reported: %s", out)
	}
	if got := string(mustReadFile(t, filepath.Join(run, "exit-status"))); got != "stale\n" {
		t.Fatalf("shell status written before exit record: %q", got)
	}
}
