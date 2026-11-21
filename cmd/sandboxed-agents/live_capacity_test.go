package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveCapacityPreflightFixture runs only the original preflight and its supplemental
// query through scripted process boundaries, so its parent can inspect the retained verdict.
func TestLiveCapacityPreflightFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_CAPACITY_PREFLIGHT") != "1" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.run(t, "preflight", suite.preflight)
}

// TestLiveCapacityFailurePreservesPreflight proves failed, malformed and missing
// supplemental engine facts cannot change a successful preflight at the fake suite seam.
func TestLiveCapacityFailurePreservesPreflight(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		response lifecycleResponse
	}{
		{"failed", lifecycleResponse{Exit: 1, Output: "private capacity query failure\n"}},
		{"malformed", lifecycleResponse{Output: "private invalid capacity payload\n"}},
		{"missing", lifecycleResponse{Output: `{"host":{"cpus":4}}`}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			directory := t.TempDir()
			program := filepath.Join(directory, "sandboxed-agents")
			if runtime.GOOS == "windows" {
				program += ".exe"
			}
			data, err := os.ReadFile(fakeExecutable)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(program, data, 0700); err != nil {
				t.Fatal(err)
			}
			cliResponses, _ := json.Marshal(map[string]lifecycleResponse{"check": {}, "version": {Output: "asset-sha256 " + strings.Repeat("a", 64) + "\n"}})
			podmanResponses, _ := json.Marshal(map[string]lifecycleResponse{"info --format json": scenario.response, "machine list --format json": {Output: `[{"Name":"test-machine","Default":true}]`}})
			values := nestedExecutables()
			for key, value := range map[string]string{"TEST_LIVE_CAPACITY_PREFLIGHT": "1", livePreparedExecutableEnv: program, "SANDBOXED_AGENTS_GROUP": "capacity-fixture", liveRecordDirEnv: directory, "TEST_SANDBOXED_AGENTS_EXECUTABLE": program, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "cli.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(cliResponses), "TEST_PODMAN_RESPONSES": string(podmanResponses)} {
				values[key] = value
			}
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveCapacityPreflightFixture$")
			command.Env = testutil.Env(fakes.Env(os.Environ()), values)
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "engine capacity unavailable") || !strings.Contains(string(output), "--- PASS: TestLiveCapacityPreflightFixture/preflight") {
				t.Fatalf("supplemental capacity changed preflight: %v\n%q", err, output)
			}
			data, err = os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Schema int `json:"schema_version"`
				Result string
				Checks []struct{ Name, Result string }
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.Schema != 2 || record.Result != "pass" || len(record.Checks) != 6 || record.Checks[0].Name != "preflight" || record.Checks[0].Result != "pass" {
				t.Fatalf("original record changed: %s", data)
			}
			for _, check := range record.Checks[1:] {
				if check.Result != "not-run" {
					t.Fatalf("unreached check changed: %+v", check)
				}
			}
			data, err = os.ReadFile(filepath.Join(directory, "operations-"+livePlatform()+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var evidence validationduration.Operations
			if err := json.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Engine != nil || strings.Contains(string(data), `"engine"`) {
				t.Fatalf("unavailable engine fabricated: %s", data)
			}
			calls := testutil.Invocations(t, filepath.Join(directory, "cli.jsonl"))
			if len(calls) != 2 || strings.Join(calls[0], " ") != "check" || strings.Join(calls[1], " ") != "version" {
				t.Fatalf("original preflight calls changed: %v", calls)
			}
		})
	}
}

// TestLiveUpTimingLeavesBuildObservationUnknown proves both build-shaped and cached
// up payloads retain one inclusive, explicitly ambiguous class at the fake CLI seam.
func TestLiveUpTimingLeavesBuildObservationUnknown(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	directory := t.TempDir()
	program := filepath.Join(directory, "sandboxed-agents")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	data, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, data, 0700); err != nil {
		t.Fatal(err)
	}
	responses, _ := json.Marshal(map[string]lifecycleResponse{"up missing": {Output: "built image private-tag\n"}, "up cached": {Output: "sandbox started\n"}})
	host := liveHost{binary: program, caseName: "lifecycle", timings: &liveTimings{}, environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_SANDBOXED_AGENTS_EXECUTABLE": program, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "calls.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(responses)})}
	for _, name := range []string{"missing", "cached"} {
		if _, err := host.cli("", "up", name); err != nil {
			t.Fatal(err)
		}
	}
	operations := host.timings.evidence.Operations
	if len(operations) != 2 {
		t.Fatalf("inclusive invocation count=%d", len(operations))
	}
	for _, operation := range operations {
		if operation.Operation != "image build or sandbox start" || operation.Source != "cli" || operation.Result != "pass" || operation.Seconds <= 0 {
			t.Errorf("untrusted output determined build attribution: %+v", operation)
		}
	}
}
