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
