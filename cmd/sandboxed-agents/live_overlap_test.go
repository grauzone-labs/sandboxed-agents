package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveSuiteRejectsInvalidOverlap proves invalid overlap is refused before any
// engine work through the nested suite's environment and fake process seams.
func TestLiveSuiteRejectsInvalidOverlap(t *testing.T) {
	for _, value := range []string{"0", "-1", "two", "1.5", "999999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			output, err := liveSuiteChild(t, fakes, map[string]string{"SANDBOXED_AGENTS_GROUP": "live-overlap", liveRecordDirEnv: t.TempDir(), "SANDBOXED_AGENTS_LIVE_OVERLAP": value})
			if err == nil || !strings.Contains(output, "SANDBOXED_AGENTS_LIVE_OVERLAP") {
				t.Fatalf("overlap %q: %v\n%s", value, err, output)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestLiveOverlapFixture exercises the suite scheduler with real fake-process
// handshakes; its parent supplies every executable and selects this fixture.
func TestLiveOverlapFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_OVERLAP_FIXTURE") != "1" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.host.removeGroupStateOnCleanup(t)
	suite.host.assertNoGroupObjectsOnCleanup(t)
	suite.run(t, "preflight", func(t *testing.T) { suite.host.mustCLI(t, "", "version") })
	if suite.overlap == 1 {
		suite.run(t, "lifecycle", func(t *testing.T) { suite.host.cleanupSandbox(t, "first"); suite.host.mustCLI(t, "", "up", "first") })
		suite.run(t, "update", func(t *testing.T) { suite.host.cleanupSandbox(t, "second"); suite.host.mustCLI(t, "", "up", "second") })
		suite.run(t, "agent", func(t *testing.T) { suite.host.mustCLI(t, "", "check", "later") })
	} else {
		if marker := os.Getenv("TEST_LIVE_OVERLAP_INTERRUPT"); marker != "" {
			suite.runConcurrent(t, []liveCase{
				{"lifecycle", func(t *testing.T, local *liveSuite) {
					local.host.mustCLI(t, "", "up", "first")
					local.baseEnsured()
					if err := os.WriteFile(marker, nil, 0600); err != nil {
						t.Fatal(err)
					}
					time.Sleep(time.Minute)
				}},
				{"update", func(t *testing.T, local *liveSuite) {
					if err := os.WriteFile(filepath.Join(filepath.Dir(marker), "others-finished"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}},
			}, nil)
			return
		}
		suite.runConcurrent(t, []liveCase{
			{"lifecycle", func(t *testing.T, local *liveSuite) {
				local.host.cleanupSandbox(t, "first")
				local.host.mustCLI(t, "", "up", "first")
				local.baseEnsured()
				if os.Getenv("TEST_LIVE_OVERLAP_FAILURE") == "1" {
					local.host.mustCLI(t, "", "check", "first")
				}
			}},
			{"update", func(t *testing.T, local *liveSuite) {
				local.host.cleanupSandbox(t, "second")
				local.host.mustCLI(t, "", "up", "second")
			}},
			{"agent", func(t *testing.T, local *liveSuite) { local.host.mustCLI(t, "", "check", "later") }},
		}, nil)
	}

}

// TestLiveOverlapAdmitsConcurrentCases proves two independent cases rendezvous
// while overlap one retains serial invocation order, through the scheduler and
// fake CLI process seam, including both case cleanup and the final group check.
func TestLiveOverlapAdmitsConcurrentCases(t *testing.T) {
	for _, overlap := range []string{"1", "2"} {
		t.Run(overlap, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			records := t.TempDir()
			ready := filepath.Join(t.TempDir(), "second")
			responses := map[string]lifecycleResponse{"version": {}, "up first": {}, "up second": {ReadyFile: ready}, "check later": {}, "remove first --volumes --force": {}, "remove second --volumes --force": {}}
			// Base ensuring must complete before other cases; the rendezvous therefore
			// sits in first's cleanup, which must keep its concurrency slot until done.
			if overlap == "2" {
				responses["remove first --volumes --force"] = lifecycleResponse{WaitFor: ready}
			}
			script, _ := json.Marshal(responses)
			program := filepath.Join(t.TempDir(), "sandboxed-agents")
			if runtime.GOOS == "windows" {
				program += ".exe"
			}
			copyFile(t, fakeExecutable, program)
			command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", livePreparedExecutableEnv: program, liveRecordDirEnv: records, "SANDBOXED_AGENTS_GROUP": "live-overlap", "SANDBOXED_AGENTS_LIVE_OVERLAP": overlap, "TEST_SANDBOXED_AGENTS_RESPONSES": string(script), "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(records, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1},"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
			started := time.Now()
			output, err := command.CombinedOutput()
			if err != nil || time.Since(started) > 5*time.Second {
				t.Fatalf("overlap %s: %v\n%s", overlap, err, output)
			}
			calls := testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
			if overlap == "1" && strings.Join(calls[2], " ") != "remove first --volumes --force" {
				t.Fatalf("serial calls %v", calls)
			}
		})
	}
}

// fixtureLiveCommand isolates lookup before constructing a nested suite fixture
// process and limits it to the supplied fake engine and test-owned host state.
func fixtureLiveCommand(t *testing.T, fakes testutil.Fakes, fixture string, values map[string]string) *exec.Cmd {
	t.Helper()
	t.Setenv("PATH", fakes.Dir)
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^"+fixture+"$", "-test.timeout=30s")
	command.Env = testutil.Env(testutil.Env(fakes.Env(os.Environ()), nestedExecutables()), values)
	return command
}

// TestLiveOverlapFailureStopsNewWork proves an observed failure refuses queued
// work without cancelling an already active case, retains its failing record,
// and completes every admitted case's cleanup at the fake CLI scheduler seam.
func TestLiveOverlapFailureStopsNewWork(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	records := t.TempDir()
	ready := filepath.Join(records, "other-ready")
	release := filepath.Join(records, "release-other")
	failed := filepath.Join(records, "failure-returned")
	failureRelease := filepath.Join(records, "release-failure")
	firstWaiting := filepath.Join(records, "first-waiting")
	program := filepath.Join(t.TempDir(), "sandboxed-agents")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	copyFile(t, fakeExecutable, program)
	responses := map[string]lifecycleResponse{
		"version": {}, "up first": {}, "check first": {ReadyFile: firstWaiting, WaitFor: failureRelease, Exit: 1, Output: "scripted first failure\n"},
		"up second": {ReadyFile: ready, WaitFor: release}, "check later": {ReadyFile: ready, WaitFor: release},
		"remove first --volumes --force": {ReadyFile: failed, WaitFor: release}, "remove second --volumes --force": {},
	}
	script, _ := json.Marshal(responses)
	command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_OVERLAP_FAILURE": "1", livePreparedExecutableEnv: program, liveRecordDirEnv: records, "SANDBOXED_AGENTS_GROUP": "live-overlap", "SANDBOXED_AGENTS_LIVE_OVERLAP": "2", liveStopAfterFailureEnv: "1", "TEST_SANDBOXED_AGENTS_RESPONSES": string(script), "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(records, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1},"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	waitLiveMarker(t, firstWaiting)
	waitLiveMarker(t, ready)
	time.Sleep(100 * time.Millisecond)
	boundedCalls := testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
	bounded := 0
	for _, call := range boundedCalls {
		if slices.Equal(call, []string{"up", "second"}) || slices.Equal(call, []string{"check", "later"}) {
			bounded++
		}
	}
	if bounded != 1 {
		t.Errorf("overlap2 exceeded its bound before any failure: %v", boundedCalls)
	}
	if err := os.WriteFile(failureRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitLiveMarker(t, failed)
	// Both slots remain occupied after failure: cleanup itself is still active.
	calls := testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
	admitted := 0
	for _, call := range calls {
		if slices.Equal(call, []string{"up", "second"}) || slices.Equal(call, []string{"check", "later"}) {
			admitted++
		}
	}
	if admitted != 1 {
		t.Errorf("admitted %d later cases while two slots occupied: %v", admitted, calls)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil || !strings.Contains(output.String(), "scripted first failure") {
		t.Fatalf("failure fixture: %v\n%s", err, &output)
	}
	calls = testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
	after := 0
	for _, call := range calls {
		if slices.Equal(call, []string{"up", "second"}) || slices.Equal(call, []string{"check", "later"}) {
			after++
		}
	}
	if after != 1 {
		t.Fatalf("new work launched after failure: %v", calls)
	}
	data, err := os.ReadFile(filepath.Join(records, liveRecordFilename(t)))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Result string
		Checks []struct{ Name, Result string }
	}
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Result != "fail" || len(record.Checks) != 6 || record.Checks[1].Result != "fail" {
		t.Fatalf("failure record: %s", data)
	}
	passed, notRun := 0, 0
	for _, check := range record.Checks[2:4] {
		if check.Result == "pass" {
			passed++
		}
		if check.Result == "not-run" {
			notRun++
		}
	}
	if passed != 1 || notRun != 1 {
		t.Fatalf("active and queued results: %s", data)
	}
	for _, name := range []string{"first", "second"} {
		if name == "second" && record.Checks[2].Result == "not-run" {
			continue
		}
		found := false
		for _, call := range calls {
			found = found || slices.Equal(call, []string{"remove", name, "--volumes", "--force"})
		}
		if !found {
			t.Errorf("missing cleanup for %s", name)
		}
	}
	groupCalls := testutil.Invocations(t, fakes.PodmanLog)
	if len(groupCalls) < 2 || !slices.Equal(groupCalls[len(groupCalls)-2], []string{"ps", "--all", "--format", "{{.Names}}"}) || !slices.Equal(groupCalls[len(groupCalls)-1], []string{"volume", "ls", "--format", "{{.Name}}"}) {
		t.Fatalf("group cleanup missing: %v", groupCalls)
	}
}

// waitLiveMarker waits for a fake-process handshake with a bounded deadline.
func waitLiveMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake process did not publish %s", filepath.Base(path))
}

// TestLiveOverlapInterruptionKeepsFailingRecord proves interrupting an active
// concurrent suite keeps the initial schema2 failing record without inventing
// completion durations, through a nested scheduler process and collector seam.
func TestLiveOverlapInterruptionKeepsFailingRecord(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	directory := t.TempDir()
	marker := filepath.Join(directory, "active")
	program := filepath.Join(directory, "sandboxed-agents")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	copyFile(t, fakeExecutable, program)
	command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_OVERLAP_INTERRUPT": marker, livePreparedExecutableEnv: program, liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "live-overlap", "SANDBOXED_AGENTS_LIVE_OVERLAP": "2", "TEST_SANDBOXED_AGENTS_RESPONSES": `{"version":{},"up first":{},"up second":{},"check later":{},"remove second --volumes --force":{}}`, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1}}`, "TMPDIR": directory, "TMP": directory, "TEMP": directory})
	var log bytes.Buffer
	command.Stdout, command.Stderr = &log, &log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	waitLiveMarker(t, marker)
	// Other operations must have finished before killing the fixture, so no
	// fake engine child can outlive the test-owned scheduler process.
	waitLiveMarker(t, filepath.Join(directory, "others-finished"))
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("interrupted suite passed")
	}
	path := filepath.Join(directory, liveRecordFilename(t))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(before, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 9 || string(fields["schema_version"]) != "2" || string(fields["result"]) != `"fail"` {
		t.Fatalf("interrupted record %s", before)
	}
	_, err = validationduration.Retain(strings.NewReader(log.String()), path, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("collector changed interrupted record: %v", err)
	}
}
