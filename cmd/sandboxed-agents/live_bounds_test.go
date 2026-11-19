package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveOperationClassBoundStreamsBeforeCompletion proves a stalled process is
// stopped at its class bound and streams prior output through liveHost.cliContext.
func TestLiveOperationClassBoundStreamsBeforeCompletion(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "heartbeat")
	environment := append(os.Environ(), "TEST_LIVE_OWNED_FIXTURE=tree", "TEST_LIVE_OWNED_MARKER="+marker)
	for key, value := range nestedExecutables() {
		environment = append(environment, key+"="+value)
	}
	lines := make(chan string, 50)
	host := liveHost{binary: os.Args[0], environment: environment,
		operationBounds: map[string]time.Duration{"inspection": 400 * time.Millisecond},
		report: func(line string) {
			select {
			case lines <- line:
			default:
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	type outcome struct {
		output string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		output, err := host.cliContext(ctx, "", "-test.run=^TestLiveCLIProcessFixture$")
		done <- outcome{output, err}
	}()
	streamed := false
	for !streamed {
		select {
		case line := <-lines:
			streamed = strings.Contains(line, "CLI child started")
		case result := <-done:
			t.Fatalf("operation finished before streaming prior output: %v", result.err)
		case <-ctx.Done():
			t.Fatal("no live output arrived before completion")
		}
	}
	result := <-done
	if !errors.Is(result.err, context.DeadlineExceeded) || !strings.Contains(result.err.Error(), "inspection") || !strings.Contains(result.err.Error(), "engine operations may still be running") || !strings.Contains(result.output, "CLI child started") {
		t.Fatalf("output=%q error=%v", result.output, result.err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("class bound did not end stalled operation: %s", elapsed)
	}
	first, _ := os.ReadFile(marker)
	time.Sleep(100 * time.Millisecond)
	last, _ := os.ReadFile(marker)
	if string(first) != string(last) {
		t.Fatal("class timeout left its owned descendant running")
	}
}

// TestLiveDirectPodmanBoundStreamsOutput proves direct engine reads use their
// harness class deadline and stream output through the fake Podman seam.
func TestLiveDirectPodmanBoundStreamsOutput(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	responses, _ := json.Marshal(map[string]lifecycleResponse{"info": {Output: "engine query started\n", DelayAfterOutputMS: 3000}})
	stream := make(chan string, 20)
	host := liveHost{environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_PODMAN_RESPONSES": string(responses)}), operationBounds: map[string]time.Duration{"inspection": 200 * time.Millisecond}, report: func(s string) { stream <- s }}
	started := time.Now()
	output, err := host.podman("info")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "inspection") || time.Since(started) > 2*time.Second || !strings.Contains(output, "engine query started") {
		t.Fatalf("direct engine query: %v\n%q", err, output)
	}
	found := false
	for len(stream) > 0 {
		line := <-stream
		found = found || strings.Contains(line, "engine query started")
	}
	if !found {
		t.Fatal("direct engine output was not streamed")
	}
}

// TestLiveTerminalClassBoundFixture exercises a stalled interactive installation
// at the suite terminal seam; its parent expects its reported deadline failure.
func TestLiveTerminalClassBoundFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_TERMINAL_BOUND") != "1" {
		return
	}
	host := liveHost{binary: os.Getenv(livePreparedExecutableEnv), environment: os.Environ(), operationBounds: map[string]time.Duration{"Tool install": 300 * time.Millisecond}, report: func(line string) { t.Log(line) }}
	terminal := host.startTerminal(t, "tools", "enable", "demo", "t3code")
	defer terminal.Close()
	terminal.Wait(5 * time.Second)
}

// TestLiveTerminalClassBoundRetainsProgress proves an interactive install ends
// at its class bound with named failure and streamed progress, at the fixture seam.
func TestLiveTerminalClassBoundRetainsProgress(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("terminal support requires Linux or Windows")
	}
	fakes := testutil.Install(t, fakeExecutable)
	program := filepath.Join(t.TempDir(), "sandboxed-agents")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	data, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(program, data, 0700); err != nil {
		t.Fatal(err)
	}
	responses, _ := json.Marshal(map[string]lifecycleResponse{"tools enable demo t3code": {Output: "install progress\n", DelayAfterOutputMS: 3000}})
	values := nestedExecutables()
	for key, value := range map[string]string{"TEST_LIVE_TERMINAL_BOUND": "1", livePreparedExecutableEnv: program, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(t.TempDir(), "terminal.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(responses)} {
		values[key] = value
	}
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveTerminalClassBoundFixture$")
	command.Env = testutil.Env(fakes.Env(os.Environ()), values)
	started := time.Now()
	output, err := command.CombinedOutput()
	if err == nil || time.Since(started) > 2*time.Second || !strings.Contains(string(output), "install progress") || !strings.Contains(string(output), "Tool install exceeded class bound") {
		t.Fatalf("terminal fixture: %v\n%q", err, output)
	}
}

// TestLiveBoundSuiteFixture exercises case failure, remaining-case selection and
// cleanup through the real suite runner and a scripted, stalled CLI process.
func TestLiveBoundSuiteFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_BOUND_SUITE") != "1" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.host.operationBounds = map[string]time.Duration{"image build or sandbox start": 300 * time.Millisecond}
	suite.host.removeGroupStateOnCleanup(t)
	suite.host.assertNoGroupObjectsOnCleanup(t)
	root := os.Getenv("XDG_STATE_HOME")
	if runtime.GOOS == "windows" {
		root = os.Getenv("LOCALAPPDATA")
	}
	if err := os.MkdirAll(filepath.Join(root, "sandboxed-agents", "group-"+suite.host.group), 0700); err != nil {
		t.Fatal(err)
	}
	suite.run(t, "preflight", func(t *testing.T) { suite.host.mustCLI(t, "", "version") })
	suite.run(t, "lifecycle", func(t *testing.T) {
		suite.host.cleanupSandbox(t, "bounded")
		suite.host.mustCLI(t, "", "up", "bounded")
	})
	suite.run(t, "update", func(t *testing.T) { suite.host.mustCLI(t, "", "list") })
}

// TestLiveBoundFailureRetainsRecordsAndCleansUp proves a class deadline leaves
// completion lines and failing schema 2, cleans sandbox/group state, and skips
// only when requested, at the nested suite/fake executable seam.
func TestLiveBoundFailureRetainsRecordsAndCleansUp(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("live records require Linux or Windows")
	}
	for _, stop := range []string{"", "1"} {
		t.Run("stop-"+stop, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
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
			responses, _ := json.Marshal(map[string]lifecycleResponse{
				"version":                          {Output: "finished prerequisite check\n"},
				"up bounded":                       {Output: "waiting for image download\n", DelayAfterOutputMS: 10000},
				"remove bounded --volumes --force": {}, "list": {},
			})
			podmanResponses, _ := json.Marshal(map[string]lifecycleResponse{"* exists *": {Exit: 1}, "ps --all --format {{.Names}}": {}, "volume ls --format {{.Name}}": {}})
			directory := t.TempDir()
			log := filepath.Join(directory, "commands.jsonl")
			values := nestedExecutables()
			for k, v := range map[string]string{
				"TEST_LIVE_BOUND_SUITE": "1", liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "bound-fixture", livePreparedExecutableEnv: program,
				liverun.StopAfterFailureEnv: stop, "TEST_SANDBOXED_AGENTS_EXECUTABLE": program, "TEST_SANDBOXED_AGENTS_LOG": log, "TEST_SANDBOXED_AGENTS_RESPONSES": string(responses), "TEST_PODMAN_RESPONSES": string(podmanResponses),
				"XDG_STATE_HOME": directory, "LOCALAPPDATA": directory,
			} {
				values[k] = v
			}
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveBoundSuiteFixture$")
			command.Env = testutil.Env(fakes.Env(os.Environ()), values)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "--- PASS: TestLiveBoundSuiteFixture/preflight") || !strings.Contains(string(output), "operation image build or sandbox start") {
				t.Fatalf("bounded suite: %v\n%q", err, output)
			}
			data, err = os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Result string
				Checks []struct{ Name, Result string }
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			results := map[string]string{}
			for _, check := range record.Checks {
				results[check.Name] = check.Result
			}
			wantUpdate := "pass"
			if stop == "1" {
				wantUpdate = "not-run"
			}
			if record.Result != "fail" || results["preflight"] != "pass" || results["lifecycle"] != "fail" || results["update"] != wantUpdate || results["agent"] != "not-run" {
				t.Fatalf("record=%s want update %s", data, wantUpdate)
			}
			removed, listed := false, false
			for _, call := range testutil.Invocations(t, log) {
				joined := strings.Join(call, " ")
				removed = removed || joined == "remove bounded --volumes --force"
				listed = listed || joined == "list"
			}
			if !removed || listed != (stop != "1") {
				t.Fatalf("cleanup=%v later case ran=%v stop=%q", removed, listed, stop)
			}
			if _, err := os.Stat(filepath.Join(directory, "sandboxed-agents", "group-bound-fixture")); !os.IsNotExist(err) {
				t.Fatalf("group state remains: %v", err)
			}
			queries := testutil.Invocations(t, fakes.PodmanLog)
			for _, kind := range []string{"container", "volume"} {
				found := false
				for _, call := range queries {
					if len(call) > 2 && call[0] == kind && call[1] == "exists" {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing %s cleanup verification: %v", kind, queries)
				}
			}
		})
	}
}

// TestLiveBoundsCoverTimingVocabulary proves harness bounds and cases exactly match
// the collector's authoritative vocabulary, so a new or misspelled class cannot hide.
func TestLiveBoundsCoverTimingVocabulary(t *testing.T) {
	classes := validationduration.OperationClasses()
	if len(classes) != len(liveOperationBounds) {
		t.Fatalf("bounds/classes differ: %v / %v", liveOperationBounds, classes)
	}
	for _, class := range classes {
		bound, ok := liveOperationBounds[class]
		if !ok || bound <= 0 || bound >= 120*time.Minute {
			t.Errorf("class %q has missing or invalid bound: %v", class, bound)
		}
	}
	cases := append([]string{"suite"}, liveCheckNames...)
	if !slices.Equal(cases, validationduration.OperationCases()) {
		t.Fatalf("harness/collector case vocabulary differs: %v / %v", cases, validationduration.OperationCases())
	}
	if liveOperationBounds["image build or sandbox start"] != 30*time.Minute {
		t.Fatal("ambiguous inclusive up bound changed")
	}
}

// TestLiveUnknownClassRefusesExecution proves a typo cannot silently receive the
// default inspection deadline at the harness operation-bound seam.
func TestLiveUnknownClassRefusesExecution(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown operation class silently accepted")
		}
	}()
	(liveHost{}).operationBound("image biuld")
}

// TestLiveImageArchiveReportsBoundedOperation proves the real image-layer check
// names its archive work and emits both progress markers at the fake Podman seam.
func TestLiveImageArchiveReportsBoundedOperation(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	responses, err := json.Marshal(map[string]lifecycleResponse{"image save --format docker-archive fixture-image": {Output: tracerArchive(t)}})
	if err != nil {
		t.Fatal(err)
	}
	var reports []string
	host := liveHost{caseName: "images", timings: &liveTimings{}, environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_PODMAN_RESPONSES": string(responses)}), report: func(line string) { reports = append(reports, line) }}
	host.assertNoBakedSSHKeys(t, "fixture-image")
	operations := host.timings.evidence.Operations
	if len(operations) != 1 || operations[0].Operation != "image archive" || operations[0].Source != "podman" || operations[0].Result != "pass" {
		t.Fatalf("image archive attribution: %+v", operations)
	}
	if len(reports) != 2 || reports[0] != "live operation image archive: started" || !strings.HasPrefix(reports[1], "live operation image archive: finished after ") {
		t.Fatalf("image archive progress: %q", reports)
	}
	elapsed, err := time.ParseDuration(strings.TrimPrefix(reports[1], "live operation image archive: finished after "))
	if err != nil || elapsed < 0 {
		t.Fatalf("archive marker has no valid elapsed duration: %q (%v)", reports[1], err)
	}
	if host.operationBound("image archive") != 10*time.Minute {
		t.Fatal("image archive did not retain its existing ten-minute budget")
	}
}

// TestLiveToolFaultInjectionIsUpdateWork proves fixture mutation and repair do
// not masquerade as read-only probes in timings at the scripted process seams.
func TestLiveToolFaultInjectionIsUpdateWork(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	cliResponses, err := json.Marshal(map[string]lifecycleResponse{"tools update demo t3code": {Exit: 1, Output: "mkdir npm/.next-fixture: permission denied\n"}})
	if err != nil {
		t.Fatal(err)
	}
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
	host := liveHost{binary: program, group: "fixture", caseName: "t3code", timings: &liveTimings{}, environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_SANDBOXED_AGENTS_EXECUTABLE": program, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(t.TempDir(), "cli.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(cliResponses)})}
	host.failedToolUpdate(t, "demo")
	operations := host.timings.evidence.Operations
	if len(operations) != 3 {
		t.Fatalf("mutation/update/repair timings: %+v", operations)
	}
	for _, operation := range operations {
		if operation.Operation != "Tool update" {
			t.Errorf("fixture mutation classified as read-only work: %+v", operation)
		}
	}
	if operations[0].Source != "podman" || operations[0].Result != "pass" || operations[1].Source != "cli" || operations[1].Result != "fail" || operations[2].Source != "podman" || operations[2].Result != "pass" {
		t.Fatalf("fixture outcomes changed: %+v", operations)
	}
}

// TestLiveImageArchiveDeadlineFixture exercises archive cancellation and reporting
// at the real layer-inspection seam under a short, test-owned class budget.
func TestLiveImageArchiveDeadlineFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_ARCHIVE_BOUND") != "1" {
		return
	}
	host := liveHost{environment: os.Environ(), operationBounds: map[string]time.Duration{"image archive": 100 * time.Millisecond}, report: func(line string) { t.Log(line) }}
	host.assertNoBakedSSHKeys(t, "fixture-image")
}

// TestLiveImageArchiveDeadlineKeepsLastOperation proves a stalled image save ends
// at its class bound and emits a final marker through a nested fake-process seam.
func TestLiveImageArchiveDeadlineKeepsLastOperation(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	responses, err := json.Marshal(map[string]lifecycleResponse{"image save --format docker-archive fixture-image": {Output: tracerArchive(t), DelayAfterOutputMS: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	values := nestedExecutables()
	values["TEST_LIVE_ARCHIVE_BOUND"] = "1"
	values["TEST_PODMAN_RESPONSES"] = string(responses)
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveImageArchiveDeadlineFixture$")
	command.Env = testutil.Env(fakes.Env(os.Environ()), values)
	started := time.Now()
	output, err := command.CombinedOutput()
	if err == nil || time.Since(started) > 2*time.Second || !strings.Contains(string(output), "live operation image archive: started") || !strings.Contains(string(output), "live operation image archive: finished") || !strings.Contains(string(output), "engine operations may still be running") {
		t.Fatalf("archive bound/progress mismatch: %v after %s\n%s", err, time.Since(started), output)
	}
}
