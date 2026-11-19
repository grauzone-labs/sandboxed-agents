package validationduration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestRetainOperationsReconcilesCaseAndPhaseSeconds proves numeric engine facts,
// the worked 8-2-3=3 case remainder, and optional phases survive the collector seam.
func TestRetainOperationsReconcilesCaseAndPhaseSeconds(t *testing.T) {
	directory := t.TempDir()
	path, err := validationduration.WriteOperations(directory, validationduration.Operations{
		SchemaVersion: 1, Kind: "live-suite-operations", Commit: "0123456789abcdef0123456789abcdef01234567", Platform: "linux",
		Engine: &validationduration.Engine{CPUs: 4, MemoryBytes: 4294967296},
		Operations: []validationduration.Operation{
			{Case: "lifecycle", Source: "cli", Operation: "image build", Result: "pass", Seconds: 2},
			{Case: "lifecycle", Source: "podman", Operation: "inspection", Result: "fail", Seconds: 3},
		},
		Cases: []validationduration.OperationCase{{Name: "lifecycle", Result: "fail", Seconds: 8}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"unaccounted_seconds":3`) {
		t.Fatalf("case remainder: %s", data)
	}
	recordPath := filepath.Join(directory, "live-suite-linux.json")
	if err := os.WriteFile(recordPath, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", Preparation: &validationduration.Interval{Start: start, End: start.Add(37 * time.Second)}, PostSuite: &validationduration.Interval{Start: start.Add(time.Hour), End: start.Add(time.Hour + 62*time.Second)}}
	if _, err := validationduration.Retain(strings.NewReader("    --- FAIL: TestLiveSuite/lifecycle (8.00s)\n"), recordPath, state); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	engine := got["engine"].(map[string]any)
	if engine["cpus"] != float64(4) || engine["memory_bytes"] != float64(4294967296) || got["preparation_seconds"] != float64(37) || got["post_suite_seconds"] != float64(62) {
		t.Fatalf("numeric evidence: %s", data)
	}
	if strings.Contains(string(data), "2026-") || strings.Contains(string(data), directory) {
		t.Fatalf("phase details leaked: %s", data)
	}
	if after, err := os.ReadFile(recordPath); err != nil || string(after) != record {
		t.Fatalf("schema 2 changed: %s; %v", after, err)
	}
}

// TestRetainPreservesDurationsWithUnsafeOperations proves invalid optional evidence
// stays rejected and unchanged while valid case durations survive through Retain.
func TestRetainPreservesDurationsWithUnsafeOperations(t *testing.T) {
	const safe = `{"schema_version":1,"kind":"live-suite-operations","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","operations":[{"case":"lifecycle","source":"cli","operation":"image build","result":"fail","seconds":2}],"cases":[{"name":"lifecycle","result":"fail","seconds":3,"unaccounted_seconds":1}]}`
	for _, scenario := range []struct{ name, data string }{
		{"unknown-output", strings.Replace(safe, `"operations":`, `"output":"private path", "operations":`, 1)},
		{"private-case", strings.Replace(safe, `"case":"lifecycle"`, `"case":"private-sandbox"`, 1)},
		{"private-operation", strings.Replace(safe, `"operation":"image build"`, `"operation":"/private/command"`, 1)},
		{"negative-time", strings.Replace(safe, `"seconds":2`, `"seconds":-2`, 1)},
		{"revision", strings.Replace(safe, "0123456789abcdef0123456789abcdef01234567", strings.Repeat("a", 40), 1)},
		{"trailing-object", safe + `{}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "live-suite-linux.json")
			operations := filepath.Join(directory, "operations-linux.json")
			durations := filepath.Join(directory, "durations-linux.json")
			for filename, data := range map[string]string{path: record, operations: scenario.data, durations: "earlier durations\n"} {
				if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			retained, err := validationduration.Retain(strings.NewReader("    --- FAIL: TestLiveSuite/lifecycle (8.00s)\n"), path, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
			if err == nil || !strings.Contains(err.Error(), "optional operation evidence") || retained != durations {
				t.Fatalf("optional evidence must report error and retained duration path: path=%q err=%v", retained, err)
			}
			for filename, want := range map[string]string{path: record, operations: scenario.data} {
				if data, err := os.ReadFile(filename); err != nil || string(data) != want {
					t.Fatalf("failed collection changed %s: %s; %v", filename, data, err)
				}
			}
			data, err := os.ReadFile(durations)
			if err != nil || !strings.Contains(string(data), `"name":"lifecycle","result":"fail","seconds":8`) {
				t.Fatalf("valid duration lost: %q; %v", data, err)
			}
		})
	}
}

// TestCollectRejectsIncompletePhaseBoundaries proves absent and reversed
// timestamps cannot fabricate phase seconds through the collector seam.
func TestCollectRejectsIncompletePhaseBoundaries(t *testing.T) {
	start := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	for _, interval := range []*validationduration.Interval{{Start: start}, {End: start}, {Start: start, End: start.Add(-time.Second)}} {
		state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", Preparation: interval}
		if _, err := validationduration.Collect(strings.NewReader(""), []byte(record), state); err == nil {
			t.Fatal("invalid phase interval accepted")
		}
	}
}
