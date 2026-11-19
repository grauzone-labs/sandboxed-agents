package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStatusPreservesCompletionPublishedDuringLiveness proves status rechecks authoritative exit artifacts after a blocked ownership read. It exercises the status command and process/artifact boundary seam.
func TestStatusPreservesCompletionPublishedDuringLiveness(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "started.json", map[string]any{"StartedAt": time.Now()})
	fifo := filepath.Join(run, "processes.json")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	out.Reset()
	go func() { done <- svc.RunDispatch(context.Background(), []string{"status", "-run-dir", run}, &out, &out) }()
	writer, err := os.OpenFile(fifo, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Opening the writer proves status already observed no exit.json and is reading ownership.
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 7, "Interrupted": false, "EndedAt": time.Now()})
	if err = os.WriteFile(filepath.Join(run, "exit-status"), []byte("7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = writer.Write([]byte(`{"RunDir":` + string(mustJSON(t, run)) + `,"Supervisor":{"PID":2147483647,"Identity":"dead"}}`))
	writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("status blocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	var ended struct {
		Code        int
		Interrupted bool
	}
	if err = json.Unmarshal(mustReadFile(t, filepath.Join(run, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 || ended.Interrupted || string(mustReadFile(t, filepath.Join(run, "exit-status"))) != "7\n" || !bytes.Contains(out.Bytes(), []byte(`"exit_code":7`)) {
		t.Fatalf("completion overwritten: %+v %s", ended, &out)
	}
}

// mustJSON encodes a fixture string without shell or JSON interpolation.
func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
