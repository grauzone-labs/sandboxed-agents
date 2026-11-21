package liverun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
