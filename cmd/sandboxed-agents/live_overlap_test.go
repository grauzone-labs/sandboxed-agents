package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
			output, err := liveSuiteChild(t, fakes, map[string]string{"SANDBOXED_AGENTS_GROUP": "live-overlap", liveRecordDirEnv: t.TempDir(), liveOverlapEnv: value})
			if err == nil || !strings.Contains(output, liveOverlapEnv) {
				t.Fatalf("overlap %q: %v\n%s", value, err, output)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestLiveOverlapFixture proves admitted cases finish and clean up while queued
// failures and interruption retain honest results at the isolated scheduler seam.
func TestLiveOverlapFixture(t *testing.T) {
	if os.Getenv("TEST_LIVE_OVERLAP_FIXTURE") != "1" {
		return
	}
	requireLiveOverlapFakes(t)
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.host.removeGroupStateOnCleanup(t)
	suite.host.assertNoGroupObjectsOnCleanup(t)
	suite.run(t, "preflight", func(t *testing.T) { suite.host.mustCLI(t, "", "version") })
	if os.Getenv("TEST_LIVE_BUILD_INPUTS") == "1" {
		suite.imagesComplete = suite.run(t, "images", func(t *testing.T) { suite.rebuildImages(t); suite.imagesRan = true })
		return
	}
	if os.Getenv("TEST_LIVE_BASE_FAILURE") == "1" {
		suite.runConcurrent(t, []liveCase{
			{"lifecycle", func(t *testing.T, local *liveSuite) { local.host.mustCLI(t, "", "up", "first"); local.baseEnsured() }},
			{"update", func(t *testing.T, local *liveSuite) { local.host.mustCLI(t, "", "check", "remaining-update") }},
			{"agent", func(t *testing.T, local *liveSuite) { local.host.mustCLI(t, "", "check", "remaining-agent") }},
			{"t3code", func(t *testing.T, local *liveSuite) { local.host.mustCLI(t, "", "check", "remaining-tool") }},
		}, func(t *testing.T, local *liveSuite) { local.rebuildImages(t); local.imagesRan = true })
		return
	}
	if mode := os.Getenv("TEST_LIVE_OBSERVE_FAILURE"); mode != "" {
		liveObservedFailureCases(t, suite, os.Getenv(liveRecordDirEnv), mode)
		return
	}
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
			program := liveFakeProgram(t)
			command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", livePreparedExecutableEnv: program, liveRecordDirEnv: records, "SANDBOXED_AGENTS_GROUP": "live-overlap", liveOverlapEnv: overlap, "TEST_SANDBOXED_AGENTS_RESPONSES": string(script), "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(records, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1},"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
			output, err := command.CombinedOutput()
			if err != nil {
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
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^"+fixture+"$", "-test.timeout=2m")
	if fixture == "TestLiveOverlapFixture" {
		values[liveOverlapFakeRootEnv] = fakes.Dir
	}
	base := testutil.Env(testutil.Env(fakes.Env(os.Environ()), nestedExecutables()), map[string]string{liveOverlapEnv: "", liveBuildJobsEnv: "", liveStopAfterFailureEnv: ""})
	command.Env = testutil.Env(base, values)
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
	program := liveFakeProgram(t)
	responses := map[string]lifecycleResponse{
		"version": {}, "up first": {}, "check first": {ReadyFile: firstWaiting, WaitFor: failureRelease, Exit: 1, Output: "scripted first failure\n"},
		"up second": {ReadyFile: ready, WaitFor: release}, "check later": {ReadyFile: ready, WaitFor: release},
		"remove first --volumes --force": {ReadyFile: failed, WaitFor: release}, "remove second --volumes --force": {},
	}
	script, _ := json.Marshal(responses)
	command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_OVERLAP_FAILURE": "1", livePreparedExecutableEnv: program, liveRecordDirEnv: records, "SANDBOXED_AGENTS_GROUP": "live-overlap", liveOverlapEnv: "2", liveStopAfterFailureEnv: "1", "TEST_SANDBOXED_AGENTS_RESPONSES": string(script), "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(records, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1},"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
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
	boundedCalls := testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
	bounded := liveLaterInvocations(boundedCalls)
	if bounded != 1 {
		t.Errorf("overlap2 exceeded its bound before any failure: %v", boundedCalls)
	}
	if err := os.WriteFile(failureRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitLiveMarker(t, failed)
	// Both slots remain occupied after failure: cleanup itself is still active.
	calls := testutil.Invocations(t, filepath.Join(records, "cli.jsonl"))
	admitted := liveLaterInvocations(calls)
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
	after := liveLaterInvocations(calls)
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
	// Thirty seconds covers the race runtime's process startup and the fake
	// rendezvous' ten-second command window, within the child's two-minute bound.
	deadline := time.Now().Add(30 * time.Second)
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
	program := liveFakeProgram(t)
	command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_OVERLAP_INTERRUPT": marker, livePreparedExecutableEnv: program, liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "live-overlap", liveOverlapEnv: "2", "TEST_SANDBOXED_AGENTS_RESPONSES": `{"version":{},"up first":{},"up second":{},"check later":{},"remove second --volumes --force":{}}`, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "cli.jsonl"), "TEST_PODMAN_RESPONSES": `{"* exists *":{"Exit":1}}`, "TMPDIR": directory, "TMP": directory, "TEMP": directory})
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

// liveFakeProgram copies the scripted CLI outside the engine PATH directory.
func liveFakeProgram(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sandboxed-agents")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	copyFile(t, fakeExecutable, path)
	return path
}

// quoteLiveJSON encodes a test-owned path as literal JSON fixture data.
func quoteLiveJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// liveObservedFailureCases holds a failed case open while its admitted sibling
// ends, allowing a queued admission decision to be observed before cleanup ends.
func liveObservedFailureCases(t *testing.T, suite *liveSuite, directory, mode string) {
	failed, otherActive := make(chan struct{}), make(chan struct{})
	failAndHold := func(t *testing.T) {
		t.Error("scripted held " + mode + " failure")
		close(failed)
		if err := os.WriteFile(filepath.Join(directory, "failed-active"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		waitLiveMarker(t, filepath.Join(directory, "release-failed"))
	}
	other := func(t *testing.T, local *liveSuite) {
		select {
		case <-failed:
			fmt.Println("LATE_ADMISSION_AFTER_FAILURE")
		default:
			close(otherActive)
			<-failed
		}
	}
	suite.runConcurrent(t, []liveCase{
		{"lifecycle", func(t *testing.T, local *liveSuite) {
			local.baseEnsured()
			<-otherActive
			if mode == "cleanup" {
				t.Cleanup(func() { failAndHold(t) })
			} else {
				failAndHold(t)
			}
		}},
		{"update", other}, {"agent", other},
	}, nil)
}

// TestLiveOverlapObservesActiveFailures proves held cleanup and nonfatal body
// failures stop queued admission as soon as another slot frees, through nested
// suite execution, streamed decisions, and fixed schema2 case results.
func TestLiveOverlapObservesActiveFailures(t *testing.T) {
	for _, mode := range []string{"cleanup", "body"} {
		t.Run(mode, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			directory := t.TempDir()
			decision := make(chan string, 4)
			log := liveOutput{lines: liveLineStream{report: func(line string) {
				if strings.Contains(line, "LATE_ADMISSION_AFTER_FAILURE") || strings.Contains(line, "not started after an earlier failure") {
					decision <- line
				}
			}}}
			command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_OBSERVE_FAILURE": mode, livePreparedExecutableEnv: liveFakeProgram(t), liveRecordDirEnv: directory, "SANDBOXED_AGENTS_GROUP": "live-observe", liveOverlapEnv: "2", liveStopAfterFailureEnv: "1", "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "cli.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": `{"version":{}}`, "TEST_PODMAN_RESPONSES": `{"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
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
			waitLiveMarker(t, filepath.Join(directory, "failed-active"))
			select {
			case line := <-decision:
				if strings.Contains(line, "LATE_ADMISSION") {
					t.Error("queued case admitted while an active case already failed")
				}
			case <-time.After(30 * time.Second):
				t.Error("queued admission decision did not arrive while failed case remained active")
			}
			if err := os.WriteFile(filepath.Join(directory, "release-failed"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("failed suite passed")
			}
			data, err := os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
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
				t.Fatalf("record %s", data)
			}
			got := []string{record.Checks[2].Result, record.Checks[3].Result}
			slices.Sort(got)
			if !slices.Equal(got, []string{"not-run", "pass"}) {
				t.Fatalf("queued/admitted records %s", data)
			}
		})
	}
}

// TestLiveOverlapFixtureRejectsFlagOnly proves the fixture flag cannot remove
// ordinary group state or run engine commands without parent-owned fake setup,
// through a nested fixture process with safe empty lookup and a state sentinel.
func TestLiveOverlapFixtureRejectsFlagOnly(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	directory := t.TempDir()
	state := filepath.Join(directory, "sandboxed-agents", "group-ordinary")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(state, "keep")
	if err := os.WriteFile(sentinel, []byte("ordinary state"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakes.Dir)
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveOverlapFixture$", "-test.timeout=1m")
	command.Env = testutil.Env(testutil.Env(fakes.Env(os.Environ()), nestedExecutables()), map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", liveRecordDirEnv: t.TempDir(), "SANDBOXED_AGENTS_GROUP": "ordinary", "XDG_STATE_HOME": directory, "LOCALAPPDATA": directory, livePreparedExecutableEnv: "", liveOverlapEnv: "", liveOverlapFakeRootEnv: ""})
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "requires isolated fake setup") {
		t.Errorf("flag-only fixture did not refuse isolation: %v\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("fixture flag removed ordinary state: %v", err)
	}
	assertNoExternalCalls(t, fakes)
}

// liveOverlapFakeRootEnv identifies the parent-owned fake lookup and state root.
const liveOverlapFakeRootEnv = "TEST_LIVE_OVERLAP_FAKE_ROOT"

// requireLiveOverlapFakes refuses fixture selection before cleanup registration
// unless lookup, host state and every executable match the parent's fake setup.
func requireLiveOverlapFakes(t *testing.T) {
	t.Helper()
	root := os.Getenv(liveOverlapFakeRootEnv)
	if !filepath.IsAbs(root) || os.Getenv("PATH") != root || os.Getenv("XDG_STATE_HOME") != root || os.Getenv("LOCALAPPDATA") != root {
		t.Fatal("live overlap fixture requires isolated fake setup")
	}
	expected, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal("live overlap fixture requires isolated fake setup")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, path := range []string{filepath.Join(root, "podman"+suffix), filepath.Join(root, "ssh"+suffix), os.Getenv(livePreparedExecutableEnv)} {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, expected) {
			t.Fatal("live overlap fixture requires isolated fake setup")
		}
	}
}

// liveLaterInvocations counts admitted sibling work in the bounded fixture.
func liveLaterInvocations(calls [][]string) int {
	count := 0
	for _, call := range calls {
		if slices.Equal(call, []string{"up", "second"}) || slices.Equal(call, []string{"check", "later"}) {
			count++
		}
	}
	return count
}

// TestLiveOverlapBaseFailurePreservesRemainingCases proves an unensured base
// falls back to the remaining serial cases unless fail-fast is selected, at the
// nested scheduler's fake CLI and six-result schema2 record seams.
func TestLiveOverlapBaseFailurePreservesRemainingCases(t *testing.T) {
	for _, stop := range []string{"", "1"} {
		t.Run("stop-"+stop, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			directory := t.TempDir()
			log := filepath.Join(directory, "cli.jsonl")
			command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_BASE_FAILURE": "1", livePreparedExecutableEnv: liveFakeProgram(t), liveRecordDirEnv: directory, liveImagesEnv: "1", "SANDBOXED_AGENTS_GROUP": "live-base", liveOverlapEnv: "2", liveStopAfterFailureEnv: stop, "TEST_SANDBOXED_AGENTS_LOG": log, "TEST_SANDBOXED_AGENTS_RESPONSES": `{"version":{},"up first":{"Exit":1},"check remaining-update":{},"check remaining-agent":{},"check remaining-tool":{},"build":{}}`, "TEST_PODMAN_RESPONSES": `{"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("failed base passed: %s", output)
			}
			want := []string{"version", "up first"}
			if stop == "" {
				want = append(want, "check remaining-update", "check remaining-agent", "check remaining-tool", "build")
			}
			var got []string
			for _, call := range testutil.Invocations(t, log) {
				got = append(got, strings.Join(call, " "))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("calls %v; want %v", got, want)
			}
			data, err := os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
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
				t.Fatalf("record %s", data)
			}
			for _, check := range record.Checks[2:] {
				want := "pass"
				if stop == "1" {
					want = "not-run"
				}
				if check.Result != want {
					t.Errorf("%s=%s; want %s", check.Name, check.Result, want)
				}
			}
		})
	}
}

// TestLiveBuildConcurrencyInput proves suite concurrency inputs are validated
// before engine work, recorded numerically, and change only the sole rebuild's
// jobs flag through the nested fake CLI and supplemental evidence seams.
func TestLiveBuildConcurrencyInput(t *testing.T) {
	for _, test := range []struct {
		name, jobs string
		wantJobs   int
		invalid    bool
	}{
		{"default", "", 1, false}, {"parallel", "3", 3, false}, {"zero", "0", 0, true}, {"negative", "-1", 0, true}, {"invalid", "no", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			directory := t.TempDir()
			log := filepath.Join(directory, "cli.jsonl")
			command := fixtureLiveCommand(t, fakes, "TestLiveOverlapFixture", map[string]string{"TEST_LIVE_OVERLAP_FIXTURE": "1", "TEST_LIVE_BUILD_INPUTS": "1", livePreparedExecutableEnv: liveFakeProgram(t), liveRecordDirEnv: directory, liveImagesEnv: "1", "SANDBOXED_AGENTS_GROUP": "live-inputs", liveOverlapEnv: "2", liveBuildJobsEnv: test.jobs, "TEST_SANDBOXED_AGENTS_LOG": log, "TEST_SANDBOXED_AGENTS_RESPONSES": `{"version":{},"build":{},"build --jobs 3":{}}`, "TEST_PODMAN_RESPONSES": `{"ps --all --format {{.Names}}":{},"volume ls --format {{.Name}}":{}}`})
			output, err := command.CombinedOutput()
			if test.invalid {
				if err == nil || !strings.Contains(string(output), liveBuildJobsEnv) {
					t.Errorf("invalid input: %v\n%s", err, output)
				}
				assertNoExternalCalls(t, fakes)
				if len(testutil.Invocations(t, log)) != 0 {
					t.Error("invalid jobs reached CLI")
				}
				return
			}
			if err != nil {
				t.Fatalf("suite: %v\n%s", err, output)
			}
			calls := testutil.Invocations(t, log)
			want := []string{"build"}
			if test.wantJobs > 1 {
				want = []string{"build", "--jobs", "3"}
			}
			if len(calls) != 2 || !slices.Equal(calls[1], want) {
				t.Fatalf("calls %v; want version then %v", calls, want)
			}
			data, err := os.ReadFile(filepath.Join(directory, "operations-"+livePlatform()+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var evidence struct {
				Overlap int `json:"case_overlap"`
				Jobs    int `json:"build_jobs"`
			}
			if err = json.Unmarshal(data, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Overlap != 2 || evidence.Jobs != test.wantJobs {
				t.Fatalf("operation evidence inputs: %s", data)
			}
		})
	}
}
