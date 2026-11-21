package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveEngineFailureFixture drives an operation failure through the real suite
// runner and checks persistence before the case's cleanup can change engine state.
func TestLiveEngineFailureFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_ENGINE_FAILURE") != "1" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	if runtime.GOOS == "windows" {
		suite.host.connection = []string{"--connection", "private-selected"}
	}
	suite.run(t, "images", func(t *testing.T) {
		t.Cleanup(func() {
			evidence := readEngineFailureEvidence(t, suite.host)
			if len(evidence.Samples) != 1 {
				t.Errorf("failure sample was not persisted before cleanup: %+v", evidence.Samples)
			}
		})
		suite.host.mustCLI(t, "", "build")
	})
}

// newEngineFailureHost isolates process lookup and enables diagnostics explicitly
// for a fake harness host, without ever reaching an installed container engine.
func newEngineFailureHost(t *testing.T, responses map[string]lifecycleResponse, platform string) (liveHost, testutil.Fakes) {
	t.Helper()
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	script, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	host := liveHost{caseName: "images", environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_PODMAN_RESPONSES": string(script)}), timings: &liveTimings{}, failures: &liveEngineFailures{
		directory: t.TempDir(), evidence: liveEngineFailureEvidence{SchemaVersion: 1, Kind: "live-engine-failures", Commit: strings.Repeat("a", 40), Platform: platform, Samples: []liveEngineFailureSample{}},
	}}
	return host, fakes
}

// readEngineFailureEvidence checks the exact artifact field boundary and reads
// a completed immediate write, independently of in-memory collector state.
func readEngineFailureEvidence(t *testing.T, host liveHost) liveEngineFailureEvidence {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(host.failures.directory, "engine-failures-"+host.failures.evidence.Platform+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence liveEngineFailureEvidence
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-selected", "private-object", "private-host", "/private/path", "private-diagnostic", "private-original"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("diagnostic artifact leaked %q", private)
		}
	}
	return evidence
}

// TestLiveEngineFailureUsesSelectedMachine proves the failure path reuses the
// engine connection while machine inspect remains a local selector query, and
// times every probe, through the real liveHost.podman and fake Podman seam.
func TestLiveEngineFailureUsesSelectedMachine(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"--connection private-selected container inspect private-object": {Exit: 23, Output: "private-original"},
		"machine inspect private-selected":                               {Output: `[{"State":"running","Name":"private-host","SSHConfig":{"IdentityPath":"/private/path"}}]`},
		"--connection private-selected info --format json":               {Output: `{"host":{"cpus":2,"memTotal":17179869184,"memFree":0,"swapTotal":8589934592,"swapFree":1234,"hostname":"private-host"}}`},
	}, "windows-11")
	host.connection = []string{"--connection", "private-selected"}
	output, err := host.podman("container", "inspect", "private-object")
	if exitStatus(t, err) != 23 || output != "private-original" {
		t.Fatalf("original direct failure changed: %v %q", err, output)
	}
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 1 {
		t.Fatalf("samples=%+v", evidence.Samples)
	}
	wantJSON := `{"case":"images","source":"podman","operation":"inspection","result":"fail","engine_query":"verified","machine_query":"verified","machine_state":"running","cpus":2,"memory_bytes":17179869184,"free_memory_bytes":0,"swap_bytes":8589934592,"free_swap_bytes":1234}`
	var want liveEngineFailureSample
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence.Samples[0], want) {
		t.Fatalf("sample=%+v; want %+v", evidence.Samples[0], want)
	}
	wantCalls := [][]string{{"--connection", "private-selected", "container", "inspect", "private-object"}, {"machine", "inspect", "private-selected"}, {"--connection", "private-selected", "info", "--format", "json"}}
	if calls := testutil.Invocations(t, fakes.PodmanLog); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("target binding: %v; want %v", calls, wantCalls)
	}
	operations := host.timings.evidence.Operations
	if len(operations) != 3 || operations[0].Result != "fail" || operations[1].Result != "pass" || operations[2].Result != "pass" {
		t.Fatalf("diagnostic invocation evidence=%+v", operations)
	}
}

// TestLiveEngineFailureKeepsUnknownFactsAbsent proves failed, malformed, missing,
// excessive and stalled observations stay unverified and private, cannot recurse,
// and preserve the original direct operation error through the fake Podman seam.
func TestLiveEngineFailureKeepsUnknownFactsAbsent(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		response lifecycleResponse
	}{
		{"failed", lifecycleResponse{Exit: 125, Output: `{"host":{"cpus":4,"memTotal":100,"memFree":20,"swapTotal":0,"swapFree":0}}`, Stderr: "private-diagnostic"}},
		{"invalid", lifecycleResponse{Output: `{"host":{"cpus":"private-host"}}`}},
		{"missing", lifecycleResponse{Output: `{"host":{"hostname":"private-host"}}`}},
		{"oversized", lifecycleResponse{Output: `{"host":{"cpus":4,"memTotal":100,"memFree":20,"swapTotal":0,"swapFree":0,"hostname":"` + strings.Repeat("private-diagnostic", 5000) + `"}}`}},
		{"stalled", lifecycleResponse{Output: `{"host":{"cpus":4,"memTotal":100,"memFree":20,"swapTotal":0,"swapFree":0}}`, DelayAfterOutputMS: 30000}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{"container inspect private-object": {Exit: 23, Output: "private-original"}, "info --format json": scenario.response}, "linux")
			var lines []string
			host.report = func(line string) { lines = append(lines, line) }
			started := time.Now()
			output, err := host.podman("container", "inspect", "private-object")
			if elapsed := time.Since(started); elapsed > 5*time.Second || exitStatus(t, err) != 23 || output != "private-original" {
				t.Fatalf("observation changed or stalled original failure: %s %v %q", elapsed, err, output)
			}
			evidence := readEngineFailureEvidence(t, host)
			want := liveEngineFailureSample{Case: "images", Source: "podman", Operation: "inspection", Result: "fail", EngineQuery: "unverified", MachineQuery: "not-applicable", MachineState: "unknown"}
			if len(evidence.Samples) != 1 || !reflect.DeepEqual(evidence.Samples[0], want) {
				t.Fatalf("fabricated missing facts: %+v", evidence.Samples)
			}
			if calls := testutil.Invocations(t, fakes.PodmanLog); len(calls) != 2 || len(host.timings.evidence.Operations) != 2 {
				t.Fatalf("recursive or untimed observation: calls=%v timings=%v", calls, host.timings.evidence.Operations)
			}
			for _, line := range lines {
				if strings.Contains(line, "private-diagnostic") || strings.Contains(line, "private-host") || strings.Contains(line, `"host"`) {
					t.Fatalf("private diagnostic query output streamed: %q", line)
				}
			}
		})
	}
}

// TestLiveEngineFailureAcceptsOnlyKnownMachineStates proves unknown or failed
// machine inspections cannot fabricate a running/stopped result through the
// selected Windows engine's failure path and fake local machine query.
func TestLiveEngineFailureAcceptsOnlyKnownMachineStates(t *testing.T) {
	for _, scenario := range []struct {
		state    string
		exit     int
		verified bool
	}{
		{"running", 0, true}, {"stopped", 0, true}, {"starting", 0, true}, {"stopping", 0, true}, {"private-host", 0, false}, {"running", 125, false},
	} {
		t.Run(scenario.state+"-exit-"+strconv.Itoa(scenario.exit), func(t *testing.T) {
			host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
				"--connection private-selected container inspect private-object": {Exit: 23},
				"machine inspect private-selected":                               {Exit: scenario.exit, Output: `[{"State":"` + scenario.state + `"}]`},
				"--connection private-selected info --format json":               {},
			}, "windows-11")
			host.connection = []string{"--connection", "private-selected"}
			host.podman("container", "inspect", "private-object")
			evidence := readEngineFailureEvidence(t, host)
			wantState, wantQuery := "unknown", "unverified"
			if scenario.verified {
				wantState, wantQuery = scenario.state, "verified"
			}
			if len(evidence.Samples) != 1 || evidence.Samples[0].MachineState != wantState || evidence.Samples[0].MachineQuery != wantQuery || len(testutil.Invocations(t, fakes.PodmanLog)) != 3 {
				t.Fatalf("unverified machine state retained: %+v", evidence.Samples)
			}
		})
	}
}

// TestLiveExpectedAbsenceDoesNotObserveEngine proves normal container/volume
// exists exit 1 and timing-only hosts never start diagnostics through liveHost.
func TestLiveExpectedAbsenceDoesNotObserveEngine(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{"container exists private-object": {Exit: 1}, "volume exists private-object": {Exit: 1}, "container inspect private-object": {Exit: 23}}, "linux")
	for _, kind := range []string{"container", "volume"} {
		if _, err := host.podman(kind, "exists", "private-object"); exitStatus(t, err) != 1 {
			t.Fatalf("normal absence changed: %v", err)
		}
	}
	if entries, err := os.ReadDir(host.failures.directory); err != nil || len(entries) != 0 {
		t.Fatalf("normal absence produced diagnostics: %v %v", entries, err)
	}
	host.failures = nil
	host.podman("container", "inspect", "private-object")
	if calls := testutil.Invocations(t, fakes.PodmanLog); len(calls) != 3 || len(host.timings.evidence.Operations) != 3 {
		t.Fatalf("timing-only fixture started probes: %v", calls)
	}
}

// TestLiveEngineFailurePersistsDespiteLaterObservationFailure proves completed
// samples remain when a later query fails, and a failed private artifact write
// cannot replace the original Podman error, through the live harness file seam.
func TestLiveEngineFailurePersistsDespiteLaterObservationFailure(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23},
		"info --format json":               {Sequence: []scriptedAnswer{{Output: `{"host":{"cpus":4,"memTotal":100,"memFree":20,"swapTotal":0,"swapFree":0}}`}, {Exit: 125}}},
	}, "linux")
	for range 2 {
		host.podman("container", "inspect", "private-object")
	}
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 2 || evidence.Samples[0].EngineQuery != "verified" || evidence.Samples[1].EngineQuery != "unverified" {
		t.Fatalf("earlier sample lost: %+v", evidence.Samples)
	}
	host.failures.directory = filepath.Join(host.failures.directory, "missing-directory")
	if _, err := host.podman("container", "inspect", "private-object"); exitStatus(t, err) != 23 {
		t.Fatalf("persistence error replaced original error: %v", err)
	}
	if len(testutil.Invocations(t, fakes.PodmanLog)) != 6 {
		t.Fatal("unexpected or recursive probes")
	}
}

// TestLiveEngineFailureUnselectedWindowsTargetRemainsUnverified proves a failed
// preflight never queries an ambient connection before selection, through liveHost.
func TestLiveEngineFailureUnselectedWindowsTargetRemainsUnverified(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{"machine list --format json": {Exit: 23}}, "windows-11")
	host.podman("machine", "list", "--format", "json")
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 1 || evidence.Samples[0].EngineQuery != "unverified" || evidence.Samples[0].MachineQuery != "unverified" || evidence.Samples[0].MemoryBytes != nil || len(testutil.Invocations(t, fakes.PodmanLog)) != 1 {
		t.Fatalf("unselected engine was queried or claimed: %+v", evidence.Samples)
	}
}

// TestLiveEngineFailureTerminalResultObservedOnce proves a failed terminal exit
// is sampled once despite repeated waits and close, through the real PTY seam.
func TestLiveEngineFailureTerminalResultObservedOnce(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("terminal support requires Linux or Windows")
	}
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{"info --format json": {DelayAfterOutputMS: 1200}}, "linux")
	// Observation may outlast the completed invocation's own class budget.
	host.operationBounds = map[string]time.Duration{"Tool install": 500 * time.Millisecond}
	program := filepath.Join(t.TempDir(), "sandboxed-agents")
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
	host.binary = program
	host.environment = testutil.Env(host.environment, map[string]string{"TEST_SANDBOXED_AGENTS_LOG": filepath.Join(t.TempDir(), "terminal.jsonl"), "TEST_SANDBOXED_AGENTS_EXIT": "23"})
	terminal := host.startTerminal(t, "tools", "enable", "demo", "t3code")
	defer terminal.Close()
	for range 2 {
		if exit, done := terminal.Wait(5 * time.Second); exit != 23 || !done {
			t.Fatalf("terminal result changed: %d %v", exit, done)
		}
	}
	terminal.Close()
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 1 || evidence.Samples[0].Operation != "Tool install" || len(testutil.Invocations(t, fakes.PodmanLog)) != 1 || len(host.timings.evidence.Operations) != 2 {
		t.Fatalf("terminal failure observed repeatedly or not timed: %+v %+v", evidence.Samples, host.timings.evidence.Operations)
	}
}

// TestLiveEngineFailureProbeTimingReconciles proves diagnostic probes have valid
// fixed timing metadata and can be persisted using #174's existing writer seam.
func TestLiveEngineFailureProbeTimingReconciles(t *testing.T) {
	host, _ := newEngineFailureHost(t, map[string]lifecycleResponse{"container inspect private-object": {Exit: 23}, "info --format json": {Exit: 125}}, "linux")
	host.podman("container", "inspect", "private-object")
	evidence := host.timings.evidence
	evidence.SchemaVersion, evidence.Kind, evidence.Commit, evidence.Platform = 1, "live-suite-operations", strings.Repeat("a", 40), "linux"
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err != nil {
		t.Fatalf("diagnostic timing facts rejected: %v", err)
	}
}

// TestLiveEngineFailureDistinguishesMissingFromMeasuredZero proves partial info
// retains measured zero free memory/swap while absent or invalid capacity stays
// absent and the query stays unverified, through the direct fake Podman seam.
func TestLiveEngineFailureDistinguishesMissingFromMeasuredZero(t *testing.T) {
	host, _ := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23},
		"info --format json":               {Output: `{"host":{"cpus":0,"memTotal":0,"memFree":0,"swapTotal":0}}`},
	}, "linux")
	host.podman("container", "inspect", "private-object")
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 1 {
		t.Fatalf("samples=%+v", evidence.Samples)
	}
	sample := evidence.Samples[0]
	if sample.EngineQuery != "unverified" || sample.CPUs != nil || sample.MemoryBytes != nil || sample.FreeSwapBytes != nil || sample.FreeMemoryBytes == nil || *sample.FreeMemoryBytes != 0 || sample.SwapBytes == nil || *sample.SwapBytes != 0 {
		t.Fatalf("unknown and measured zero conflated: %+v", sample)
	}
}

// TestLiveEngineFailureConcurrentSnapshotsPersistAllSamples proves concurrent
// failures start their reads immediately and atomically retain both completed
// samples through the real liveHost failure path and fake engine/file seams.
func TestLiveEngineFailureConcurrentSnapshotsPersistAllSamples(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23},
		"info --format json":               {WaitFor: release, Output: `{"host":{"cpus":4,"memTotal":100,"memFree":20,"swapTotal":0,"swapFree":0}}`},
	}, "linux")
	var group sync.WaitGroup
	results := make(chan error, 2)
	defer func() {
		os.WriteFile(release, nil, 0600)
		group.Wait()
	}()
	for range 2 {
		group.Go(func() {
			_, err := host.podman("container", "inspect", "private-object")
			results <- err
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, _ := os.ReadFile(fakes.PodmanLog)
		if strings.Count(string(data), `["info","--format","json"]`+"\n") == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("one failure's query blocked the other snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	for range 2 {
		if err := <-results; exitStatus(t, err) != 23 {
			t.Fatalf("original concurrent failure changed: %v", err)
		}
	}
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 2 || evidence.Samples[0].EngineQuery != "verified" || evidence.Samples[1].EngineQuery != "verified" || len(testutil.Invocations(t, fakes.PodmanLog)) != 4 || len(host.timings.evidence.Operations) != 4 {
		t.Fatalf("concurrent observations lost: %+v", evidence.Samples)
	}
}

// TestLiveEngineFailureRetainsSanitizedSnapshot proves literal engine facts are
// retained immediately without private detail or changes to the failing case or
// schema 2, through the nested suite and fake executable/Podman boundary.
func TestLiveEngineFailureRetainsSanitizedSnapshot(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("live records require Linux or Windows")
	}
	fakes := testutil.Install(t, fakeExecutable)
	// exec.Command resolves Podman against the process PATH, before cmd.Env.
	t.Setenv("PATH", fakes.Dir)
	program := filepath.Join(t.TempDir(), "sandboxed-agents")
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
	responses, _ := json.Marshal(map[string]lifecycleResponse{"build": {Exit: 23, Output: "original engine failure sentinel\n"}})
	info := lifecycleResponse{Output: `{"host":{"cpus":4,"memTotal":4294967296,"memFree":123456789,"swapTotal":0,"swapFree":0,"hostname":"private-engine-sentinel","store":"/private/engine/path"}}`}
	podmanResponses, _ := json.Marshal(map[string]lifecycleResponse{
		"info --format json": info, "--connection private-selected info --format json": info,
		"machine inspect private-selected": {Output: `[{"State":"running"}]`},
	})
	directory := t.TempDir()
	values := nestedExecutables()
	for key, value := range map[string]string{
		"TEST_LIVE_ENGINE_FAILURE": "1", liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "engine-fixture", livePreparedExecutableEnv: program,
		"TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "commands.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(responses), "TEST_PODMAN_RESPONSES": string(podmanResponses),
	} {
		values[key] = value
	}
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveEngineFailureFixture$")
	command.Env = testutil.Env(fakes.Env(os.Environ()), values)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "original engine failure sentinel") || !strings.Contains(string(output), "exit status 23") || !strings.Contains(string(output), "--- FAIL: TestLiveEngineFailureFixture/images") {
		t.Fatalf("original failure changed: %v\n%q", err, output)
	}
	data, err = os.ReadFile(filepath.Join(directory, "engine-failures-"+livePlatform()+".json"))
	if err != nil {
		t.Fatalf("no immediate engine failure evidence: %v\n%q", err, output)
	}
	var evidence struct {
		SchemaVersion          int `json:"schema_version"`
		Kind, Commit, Platform string
		Samples                []map[string]any
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	commit, err := liveCommit("../..")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"case": "images", "source": "cli", "operation": "rebuild", "result": "fail", "engine_query": "verified", "machine_query": "not-applicable", "machine_state": "unknown", "cpus": float64(4), "memory_bytes": float64(4294967296), "free_memory_bytes": float64(123456789), "swap_bytes": float64(0), "free_swap_bytes": float64(0)}
	if runtime.GOOS == "windows" {
		want["machine_query"], want["machine_state"] = "verified", "running"
	}
	if evidence.SchemaVersion != 1 || evidence.Kind != "live-engine-failures" || evidence.Commit != commit || evidence.Platform != livePlatform() || len(evidence.Samples) != 1 || !reflect.DeepEqual(evidence.Samples[0], want) {
		t.Fatalf("engine failure evidence=%s; want one literal sanitized sample %v", data, want)
	}
	for _, private := range []string{"private-engine-sentinel", "/private/engine/path", directory, fakes.Dir, "original engine failure sentinel", "engine-fixture"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private detail retained: %q", private)
		}
	}
	record, err := os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 9 || string(fields["schema_version"]) != "2" || string(fields["result"]) != `"fail"` || !strings.Contains(string(fields["checks"]), `{"name":"images","result":"fail"}`) {
		t.Fatalf("schema 2/case verdict changed: %s", record)
	}
	wantCalls := [][]string{{"info", "--format", "json"}}
	if runtime.GOOS == "windows" {
		wantCalls = [][]string{{"machine", "inspect", "private-selected"}, {"--connection", "private-selected", "info", "--format", "json"}}
	}
	if calls := testutil.Invocations(t, fakes.PodmanLog); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("unexpected diagnostic probes: %v", calls)
	}
}

// TestLiveEngineFailureCapsConcurrentDeadEngineProbes proves one run admits only
// four snapshots, exposes suppression, and preserves failures and timing at the
// fake process and sanitized file seams even when concurrent queries stall.
func TestLiveEngineFailureCapsConcurrentDeadEngineProbes(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"--connection private-selected container inspect private-object": {Exit: 23, Output: "private-original"},
		"machine inspect private-selected":                               {DelayAfterOutputMS: 30000},
		"--connection private-selected info --format json":               {DelayAfterOutputMS: 30000},
	}, "windows-11")
	host.connection = []string{"--connection", "private-selected"}
	var group sync.WaitGroup
	results := make(chan bool, 12)
	started := time.Now()
	for range 12 {
		group.Go(func() {
			output, err := host.podman("container", "inspect", "private-object")
			exit, ok := err.(*exec.ExitError)
			results <- ok && exit.ExitCode() == 23 && output == "private-original"
		})
	}
	group.Wait()
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("concurrent diagnostic budget exceeded: %s", elapsed)
	}
	for range 12 {
		if !<-results {
			t.Fatal("diagnostics replaced original result")
		}
	}
	evidence := readEngineFailureEvidence(t, host)
	data, err := os.ReadFile(filepath.Join(host.failures.directory, "engine-failures-windows-11.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if len(evidence.Samples) != 4 || string(fields["samples_truncated"]) != "true" {
		t.Fatalf("unbounded or silently incomplete failure coverage: %s", data)
	}
	for _, sample := range evidence.Samples {
		if sample.EngineQuery != "unverified" || sample.MachineQuery != "unverified" || sample.CPUs != nil || sample.MemoryBytes != nil {
			t.Fatalf("dead engine facts fabricated: %+v", sample)
		}
	}
	calls := testutil.Invocations(t, fakes.PodmanLog)
	if len(calls) != 20 || len(host.timings.evidence.Operations) != 20 {
		t.Fatalf("probe budget exceeded or timings lost: calls=%d timings=%d", len(calls), len(host.timings.evidence.Operations))
	}
	// The exhausted run must issue neither another query nor another rewrite.
	// Removing the verified report exposes even an identical rewrite.
	path := filepath.Join(host.failures.directory, "engine-failures-windows-11.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := host.podman("container", "inspect", "private-object"); exitStatus(t, err) != 23 {
		t.Fatalf("suppression changed original result: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exhausted run rewrote evidence: %v", err)
	}
	if calls := len(testutil.Invocations(t, fakes.PodmanLog)); calls != 21 {
		t.Fatalf("exhausted run queried the engine: %d calls", calls)
	}
}

// TestLiveEngineFailureRetriesUnsavedTruncation proves a suppression flag whose
// write failed is written by a later failure without reopening the probe budget,
// and that once it is on disk later failures write nothing, at the fake process
// and sanitized file seams.
func TestLiveEngineFailureRetriesUnsavedTruncation(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23, Output: "private-original"},
		"info --format json":               {Output: `{"host":{"cpus":4,"memTotal":100,"memFree":0,"swapTotal":0,"swapFree":0}}`},
	}, "linux")
	persistFailures := 0
	host.report = func(line string) {
		if line == "live engine failure evidence could not be persisted" {
			persistFailures++
		}
	}
	fail := func() {
		t.Helper()
		if _, err := host.podman("container", "inspect", "private-object"); exitStatus(t, err) != 23 {
			t.Fatalf("diagnostics changed original result: %v", err)
		}
	}
	for range 4 {
		fail()
	}
	directory := host.failures.directory
	path := filepath.Join(directory, "engine-failures-linux.json")
	host.failures.directory = filepath.Join(directory, "missing")
	fail()
	host.failures.directory = directory
	if persistFailures != 1 || readEngineFailureEvidence(t, host).SamplesTruncated {
		t.Fatalf("unwritable truncation: persist failures=%d", persistFailures)
	}
	fail()
	if evidence := readEngineFailureEvidence(t, host); persistFailures != 1 || len(evidence.Samples) != 4 || !evidence.SamplesTruncated {
		t.Fatalf("truncation was not retried: persist failures=%d samples=%d truncated=%v", persistFailures, len(evidence.Samples), evidence.SamplesTruncated)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fail()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved truncation was rewritten: %v", err)
	}
	if calls := testutil.Invocations(t, fakes.PodmanLog); len(calls) != 11 {
		t.Fatalf("suppression reopened the probe budget: %d calls", len(calls))
	}
}

// TestLiveEngineFailureRetriesSampleWriteAfterSavedTruncation proves a sample
// whose write failed after another failure had already saved the truncation
// flag is written by the next failure once storage recovers, without another
// probe, at the fake process and sanitized file seams.
func TestLiveEngineFailureRetriesSampleWriteAfterSavedTruncation(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23, Output: "private-original"},
		"info --format json":               {Output: `{"host":{"cpus":4,"memTotal":100,"memFree":0,"swapTotal":0,"swapFree":0}}`},
	}, "linux")
	host.report = func(string) {}
	// Three slots are already in flight, and a failure over budget has saved the
	// truncation flag meanwhile, as concurrent probes can leave the collector.
	host.failures.reserved = 3
	host.failures.evidence.SamplesTruncated = true
	host.failures.truncationSaved = true
	fail := func() {
		t.Helper()
		if _, err := host.podman("container", "inspect", "private-object"); exitStatus(t, err) != 23 {
			t.Fatalf("diagnostics changed original result: %v", err)
		}
	}
	directory := host.failures.directory
	host.failures.directory = filepath.Join(directory, "missing")
	fail()
	host.failures.directory = directory
	fail()
	if evidence := readEngineFailureEvidence(t, host); len(evidence.Samples) != 1 || !evidence.SamplesTruncated {
		t.Fatalf("unsaved sample was not retried: samples=%d truncated=%v", len(evidence.Samples), evidence.SamplesTruncated)
	}
	if calls := testutil.Invocations(t, fakes.PodmanLog); len(calls) != 3 {
		t.Fatalf("the retry reopened the probe budget: %d calls", len(calls))
	}
}

// TestLiveEngineFailureExpectedRollbackKeepsOtherDiagnostics proves the precise
// injected rollback omits a false sample while later failures remain observable
// and both invocations keep their timings at the fake CLI and Podman seams.
func TestLiveEngineFailureExpectedRollbackKeepsOtherDiagnostics(t *testing.T) {
	host, fakes := newEngineFailureHost(t, map[string]lifecycleResponse{
		"container inspect private-object": {Exit: 23, Output: "private-original"},
		"info --format json":               {Output: `{"host":{"cpus":4,"memTotal":100,"memFree":0,"swapTotal":0,"swapFree":0}}`},
	}, "linux")
	program := filepath.Join(t.TempDir(), "sandboxed-agents")
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
	host.binary = program
	host.environment = testutil.Env(host.environment, map[string]string{
		"TEST_SANDBOXED_AGENTS_LOG":  filepath.Join(t.TempDir(), "cli.jsonl"),
		"TEST_SANDBOXED_AGENTS_EXIT": "23", "TEST_SANDBOXED_AGENTS_OUTPUT": "readiness wait failed; restored sandbox",
	})
	failing := failingLiveUpdateHost(t, host, "2222")
	output, err := failing.cli("", "update", "demo", "--force")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || output != "readiness wait failed; restored sandbox" {
		t.Fatalf("expected rollback result changed: %v %q", err, output)
	}
	if entries, err := os.ReadDir(host.failures.directory); err != nil || len(entries) != 0 {
		t.Fatalf("intentional rollback produced a false failure sample: %v %v", entries, err)
	}
	if _, err := host.podman("container", "inspect", "private-object"); exitStatus(t, err) != 23 {
		t.Fatalf("subsequent failure changed: %v", err)
	}
	evidence := readEngineFailureEvidence(t, host)
	if len(evidence.Samples) != 1 || evidence.Samples[0].Source != "podman" || len(testutil.Invocations(t, fakes.PodmanLog)) != 2 || len(host.timings.evidence.Operations) != 3 || host.timings.evidence.Operations[0].Result != "fail" {
		t.Fatalf("other diagnostics/timings hidden: %+v %+v", evidence, host.timings.evidence.Operations)
	}
}

// TestLiveEngineFailureExpectedToolFixture exercises the actual expected Tool
// failure assertion inside a recorded case for its fake-process parent test.
func TestLiveEngineFailureExpectedToolFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_EXPECTED_TOOL_FAILURE") != "1" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.run(t, "t3code", func(t *testing.T) { suite.host.failedToolUpdate(t, "demo") })
}

// TestLiveEngineFailureExpectedToolKeepsCaseVerdict proves intentional staging
// failure omits probes while wrong failure or unexpected success is sampled and
// still fails the original assertion, preserving schema 2, repair and timings at
// the nested fake CLI and sanitized record seam.
func TestLiveEngineFailureExpectedToolKeepsCaseVerdict(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("live records require Linux or Windows")
	}
	for _, scenario := range []struct {
		name, output, result string
		exit                 int
	}{
		{"expected", ".next-private: permission denied", "pass", 23},
		{"wrong-failure", "cannot allocate memory; private-original", "fail", 23},
		{"unexpected-success", "private-original", "fail", 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			directory := t.TempDir()
			program := filepath.Join(t.TempDir(), "sandboxed-agents")
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
			responses, _ := json.Marshal(map[string]lifecycleResponse{"tools update demo t3code": {Exit: scenario.exit, Output: scenario.output}})
			podmanResponses, _ := json.Marshal(map[string]lifecycleResponse{"exec * break-install": {}, "exec * repair-install": {}, "info --format json": {Output: `{"host":{"cpus":4,"memTotal":100,"memFree":0,"swapTotal":0,"swapFree":0}}`}})
			values := nestedExecutables()
			for key, value := range map[string]string{
				"TEST_LIVE_EXPECTED_TOOL_FAILURE": "1", liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "engine-fixture", livePreparedExecutableEnv: program,
				"TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "commands.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(responses), "TEST_PODMAN_RESPONSES": string(podmanResponses),
			} {
				values[key] = value
			}
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveEngineFailureExpectedToolFixture$")
			command.Env = testutil.Env(fakes.Env(os.Environ()), values)
			output, err := command.CombinedOutput()
			if (err == nil) != (scenario.result == "pass") || (scenario.result == "fail" && (!strings.Contains(string(output), "expected deterministic Tool staging failure") || !strings.Contains(string(output), "private-original") || (scenario.exit != 0 && !strings.Contains(string(output), "exit status 23")))) {
				t.Fatalf("original assertion changed: %v\n%q", err, output)
			}
			data, err = os.ReadFile(filepath.Join(directory, "engine-failures-"+livePlatform()+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var evidence liveEngineFailureEvidence
			wantSamples, diagnosticReads := 0, 0
			if scenario.result == "fail" {
				wantSamples = 1
				if livePlatform() == "linux" {
					diagnosticReads = 1
				}
			}
			if err := json.Unmarshal(data, &evidence); err != nil || len(evidence.Samples) != wantSamples || evidence.SamplesTruncated {
				t.Fatalf("expected/mismatched call sampled incorrectly: %s %v", data, err)
			}
			if wantSamples == 1 {
				sample := evidence.Samples[0]
				if sample.Case != "t3code" || sample.Source != "cli" || sample.Operation != "Tool update" || sample.Result != "fail" || strings.Contains(string(data), "private-original") || strings.Contains(string(data), "cannot allocate memory") {
					t.Fatalf("mismatched failure lost attribution or leaked output: %s", data)
				}
				if diagnosticReads == 1 && (sample.EngineQuery != "verified" || sample.CPUs == nil || *sample.CPUs != 4 || sample.MemoryBytes == nil || *sample.MemoryBytes != 100) {
					t.Fatalf("mismatched failure facts lost: %s", data)
				}
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if len(calls) != 2+diagnosticReads || calls[0][len(calls[0])-1] != "break-install" || calls[len(calls)-1][len(calls[len(calls)-1])-1] != "repair-install" {
				t.Fatalf("probes added or repair lost: %v", calls)
			}
			data, err = os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(data, &record); err != nil || len(record) != 9 || string(record["schema_version"]) != "2" || string(record["result"]) != strconv.Quote(scenario.result) || !strings.Contains(string(record["checks"]), `{"name":"t3code","result":`+strconv.Quote(scenario.result)+`}`) {
				t.Fatalf("original case/schema changed: %s %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(directory, "operations-"+livePlatform()+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var timings validationduration.Operations
			if err := json.Unmarshal(data, &timings); err != nil || len(timings.Operations) != 3+diagnosticReads || timings.Operations[1].Source != "cli" || timings.Operations[1].Result != map[bool]string{true: "pass", false: "fail"}[scenario.exit == 0] {
				t.Fatalf("expected invocation timings lost: %s %v", data, err)
			}
		})
	}
}
