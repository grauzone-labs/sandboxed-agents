package validationduration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestRetainActualInterruptedVerboseOutput proves finished subtests whose
// completion lines remain buffered receive no invented durations through a real
// verbose Go test subprocess, retention, and stable-gate record boundary.
func TestRetainActualInterruptedVerboseOutput(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "interrupted.test.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, "./testdata/interrupted")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build offline fixture: %v\n%s", err, output)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	child := exec.CommandContext(ctx, binary, "-test.v", "-test.run=^TestLiveSuite$", "-test.timeout=1m")
	child.Env = testutil.Env(os.Environ(), map[string]string{"TEST_DURATION_DIRECTORY": directory})
	var captured bytes.Buffer
	child.Stdout, child.Stderr = &captured, &captured
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("later case did not reach its marker")
		case <-time.After(10 * time.Millisecond):
		}
	}
	path := filepath.Join(directory, "live-suite-linux.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("killed child passed")
	}
	log := captured.String()
	for _, marker := range []string{"=== RUN   TestLiveSuite/preflight", "offline preflight finished", "=== RUN   TestLiveSuite/lifecycle", "offline lifecycle finished", "=== RUN   TestLiveSuite/agent", "OFFLINE_LATER_CASE_MARKER"} {
		if !strings.Contains(log, marker) {
			t.Fatalf("missing %q in actual output:\n%s", marker, log)
		}
	}
	// Go's ordinary verbose output buffers child completion reports until the
	// parent ends. A finished body is not an emitted completion or a duration.
	if strings.Contains(log, "--- PASS:") || strings.Contains(log, "--- FAIL:") {
		t.Fatalf("unexpected completion before parent ended:\n%s", log)
	}
	output, err := validationduration.Retain(strings.NewReader(log), path, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	timing, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(timing, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["cases"]) != "[]" {
		t.Fatalf("invented durations for absent completion lines: %s", timing)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("changed schema-2 record: %s; %v", after, err)
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(after, &source); err != nil {
		t.Fatal(err)
	}
	if len(source) != 9 || string(source["schema_version"]) != "2" || string(source["result"]) != `"fail"` {
		t.Fatalf("invalid failing record: %s", after)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	evidence := release.StableGateEvidence{Tag: "v1.0.0", Commit: commit, Preview: &release.PreviewEvidence{
		Tag: "v1.0.0-preview.20261108.1", Commit: commit, Records: map[string][]byte{"live-suite-linux.json": after, filepath.Base(output): timing},
	}}
	if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "live-suite-linux.json: validation gate did not pass") {
		t.Fatalf("interrupted record did not keep gate failing: %v", err)
	}
}
