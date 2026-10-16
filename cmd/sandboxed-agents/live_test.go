package main_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// The live suite is one opt-in entry point. SANDBOXED_AGENTS_LIVE_TESTS=1 selects
// it, SANDBOXED_AGENTS_GROUP names its dedicated controller group, and
// SANDBOXED_AGENTS_LIVE_SUMMARY_DIR receives the platform's validation record.
// Run it with SANDBOXED_AGENTS_LIVE_TESTS=1 SANDBOXED_AGENTS_GROUP=live
// SANDBOXED_AGENTS_LIVE_SUMMARY_DIR=DIR go test ./cmd/sandboxed-agents
// -run '^TestLiveSuite$' -timeout 120m -v, adding
// SANDBOXED_AGENTS_LIVE_IMAGES_TESTS=1 to select the image coverage.
const liveSummaryDirEnv = "SANDBOXED_AGENTS_LIVE_SUMMARY_DIR"

// controllerGroupPattern repeats the controller group rule of ADR-0005.
var controllerGroupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// liveCheckNames are the fixed check identifiers of the record, in run order.
var liveCheckNames = []string{"preflight", "lifecycle", "update", "agent", "images"}

// liveSuite carries one run's inputs, host, and check results.
type liveSuite struct {
	host                                      liveHost
	buildDirectory, summaryDirectory          string
	commit, platform, assetHash               string
	imagesSelected, imagesRan, imagesComplete bool
	results                                   map[string]string
}

// TestLiveSuite runs the live cases against real Podman on the selected host in a dedicated controller group and writes the platform's validation record, only when explicitly opted in.
func TestLiveSuite(t *testing.T) {
	if os.Getenv(testutil.LiveTestsEnv) != "1" {
		t.Skip("set " + testutil.LiveTestsEnv + "=1 to opt in")
	}
	t.Log("live test body selected")
	suite := newLiveSuite(t)
	if !suite.run(t, "preflight", suite.preflight) {
		return
	}
	suite.host.removeGroupStateOnCleanup(t)
	suite.host.assertNoGroupObjectsOnCleanup(t)
	suite.run(t, "lifecycle", suite.lifecycle)
	suite.run(t, "update", suite.update)
	suite.run(t, "agent", suite.agent)
	if suite.imagesSelected {
		suite.imagesComplete = suite.run(t, "images", suite.images)
	} else {
		t.Run("images", func(t *testing.T) { t.Skip("set " + liveImagesEnv + "=1 to select host-wide image rebuild coverage") })
	}
}

// newLiveSuite checks the run's inputs, resolves the checkout's commit from the
// git metadata files, and writes the failing record, all before any program runs.
func newLiveSuite(t *testing.T) *liveSuite {
	t.Helper()
	suite := &liveSuite{host: liveHost{binary: executable, environment: os.Environ()}, results: map[string]string{}}
	for _, name := range liveCheckNames {
		suite.results[name] = "not-run"
	}
	suite.platform = map[string]string{"linux": "linux", "windows": "windows-11"}[runtime.GOOS]
	if suite.platform == "" {
		t.Fatalf("the live suite runs on Linux amd64 or Windows 11 x64, not %s", runtime.GOOS)
	}
	group := os.Getenv("SANDBOXED_AGENTS_GROUP")
	if group == "" || group == "default" || !controllerGroupPattern.MatchString(group) {
		t.Fatalf("the live suite needs SANDBOXED_AGENTS_GROUP set to a dedicated controller group matching ^[a-z0-9][a-z0-9-]*$ other than default; got %q", group)
	}
	suite.host.group = group
	suite.summaryDirectory = os.Getenv(liveSummaryDirEnv)
	if suite.summaryDirectory == "" {
		t.Fatalf("the live suite needs %s set to the directory that receives its validation record", liveSummaryDirEnv)
	}
	if err := os.MkdirAll(suite.summaryDirectory, 0o700); err != nil {
		t.Fatalf("%s: %v", liveSummaryDirEnv, err)
	}
	suite.imagesSelected = os.Getenv(liveImagesEnv) == "1"
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	suite.commit, err = liveCommit(root)
	if err != nil {
		t.Fatalf("resolve the checkout's commit: %v", err)
	}
	suite.buildDirectory = t.TempDir()
	suite.writeRecord(t, false)
	t.Cleanup(func() { suite.writeRecord(t, true) })
	return suite
}

// run records a case's result from its subtest outcome.
func (suite *liveSuite) run(t *testing.T, name string, body func(*testing.T)) bool {
	passed := t.Run(name, body)
	suite.results[name] = map[bool]string{true: "pass", false: "fail"}[passed]
	return passed
}

// writeRecord replaces the platform's record. The result is pass only in the
// final write, when no case failed and the suite itself reported no error.
func (suite *liveSuite) writeRecord(t *testing.T, final bool) {
	t.Helper()
	record := release.LiveSuiteRecord{
		Commit: suite.commit, Platform: suite.platform, Result: "fail",
		ImagePartSelected: suite.imagesSelected, ImagePartRan: suite.imagesRan, ImageCoverageComplete: suite.imagesComplete,
	}
	passed := final && !t.Failed()
	for _, name := range liveCheckNames {
		result := suite.results[name]
		passed = passed && result != "fail"
		record.Checks = append(record.Checks, release.LiveSuiteCheck{Name: name, Result: result})
	}
	if passed {
		record.Result = "pass"
	}
	path, err := release.WriteLiveSuiteRecord(suite.summaryDirectory, record)
	if err != nil {
		t.Fatalf("write the live-suite record: %v", err)
	}
	t.Logf("live-suite record (%s) written to %s", record.Result, path)
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// liveCommit resolves HEAD from the checkout's git metadata files without
// running git, following a worktree's gitdir and commondir indirections.
func liveCommit(root string) (string, error) {
	gitDirectory := filepath.Join(root, ".git")
	info, err := os.Stat(gitDirectory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(gitDirectory)
		if err != nil {
			return "", err
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return "", fmt.Errorf(".git file names no gitdir")
		}
		gitDirectory = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(root, gitDirectory)
		}
	}
	commonDirectory := gitDirectory
	if data, err := os.ReadFile(filepath.Join(gitDirectory, "commondir")); err == nil {
		commonDirectory = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDirectory) {
			commonDirectory = filepath.Join(gitDirectory, commonDirectory)
		}
	}
	data, err := os.ReadFile(filepath.Join(gitDirectory, "HEAD"))
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(data))
	if commitPattern.MatchString(head) {
		return head, nil
	}
	ref, ok := strings.CutPrefix(head, "ref:")
	if !ok {
		return "", fmt.Errorf("HEAD is neither a commit nor a ref: %q", head)
	}
	ref = strings.TrimSpace(ref)
	if data, err := os.ReadFile(filepath.Join(commonDirectory, filepath.FromSlash(ref))); err == nil {
		if commit := strings.TrimSpace(string(data)); commitPattern.MatchString(commit) {
			return commit, nil
		}
	}
	packed, err := os.ReadFile(filepath.Join(commonDirectory, "packed-refs"))
	if err != nil {
		return "", fmt.Errorf("ref %s is neither loose nor packed: %v", ref, err)
	}
	for _, line := range strings.Split(string(packed), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref && commitPattern.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("ref %s is neither loose nor packed", ref)
}

// preflight runs check with the development executable, selects the Windows
// connection, and compiles the prepared executable every later case uses.
func (suite *liveSuite) preflight(t *testing.T) {
	// Check the prerequisites before compiling the embedded manager or creating
	// anything. An opted-in run on an unsupported host fails rather than claiming
	// coverage from a skip.
	suite.host.mustCLI(t, "", "check")
	suite.host.selectConnection(t)
	suite.host.binary = buildLiveExecutable(t, suite.buildDirectory)
	version := suite.host.mustCLI(t, "", "version")
	hashes := regexp.MustCompile(`(?m)^asset-sha256 ([a-f0-9]{64})\r?$`).FindStringSubmatch(version)
	if len(hashes) != 2 {
		t.Fatalf("version omitted the asset hash: %s", version)
	}
	suite.assetHash = hashes[1]
}

// lifecycle creates, lists, stops, starts, checks, and removes one sandbox on
// the existing base image, and shows its SSH server before and after the restart.
func (suite *liveSuite) lifecycle(t *testing.T) {
	host := suite.host
	const name = "live-lifecycle"
	host.cleanupSandbox(t, name)
	host.mustCLI(t, "", "up", name)
	key := host.sshHostKey(t, name)
	host.assertSSHServer(t, name, key)
	host.assertListed(t, name, "running")
	host.mustCLI(t, "", "check", name)
	host.mustCLI(t, "", "stop", name)
	host.assertListed(t, name, "stopped")
	host.mustCLI(t, "", "start", name)
	host.assertListed(t, name, "running")
	if restarted := host.sshHostKey(t, name); restarted != key {
		t.Fatalf("restart changed the SSH host key: before=%q after=%q", key, restarted)
	}
	host.assertSSHServer(t, name, key)
	host.mustCLI(t, "", "remove", name, "--volumes")
	host.assertListed(t, name, "")
	host.assertNoSandboxObjects(t, name)
}

// images runs the host-wide image coverage: one sandbox per toolchain set and
// one without, the suite's only build, and the outdated marks it leaves.
func (suite *liveSuite) images(t *testing.T) {
	t.Log("live test body selected (images; build renews images across controller groups)")
	liveImageCoverage(t, suite)
}

// liveSuiteChild runs TestLiveSuite in a nested test process against the fake
// harness with the parent's built binaries and the given variables.
func liveSuiteChild(t *testing.T, fakes testutil.Fakes, values map[string]string) (string, error) {
	t.Helper()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		testutil.LiveTestsEnv: "1", liveImagesEnv: "", liveSummaryDirEnv: "",
		"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable, "TEST_RELEASE_EXECUTABLE": releaseExecutable,
	}
	maps.Copy(environment, values)
	command := exec.Command(testExecutable, "-test.v", "-test.run=^TestLiveSuite$")
	command.Env = testutil.Env(fakes.Env(os.Environ()), environment)
	output, err := command.CombinedOutput()
	return string(output), err
}

// liveSummaryFilename is the record file name of the test host's platform.
func liveSummaryFilename(t *testing.T) string {
	t.Helper()
	platform := map[string]string{"linux": "linux", "windows": "windows-11"}[runtime.GOOS]
	filename, err := release.LiveSuiteRecordFilename(platform)
	if err != nil {
		t.Skipf("no live-suite record for %s", runtime.GOOS)
	}
	return filename
}

// TestLiveSuiteOptInSelectsOnlyExplicitOne proves only an opt-in value of "1" selects the live suite, whose first Podman use is the preflight, through the nested test process and fake Podman.
func TestLiveSuiteOptInSelectsOnlyExplicitOne(t *testing.T) {
	for _, value := range []string{"0", "true", "1"} {
		t.Run(value, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			summaries := t.TempDir()
			output, err := liveSuiteChild(t, fakes, map[string]string{testutil.LiveTestsEnv: value, "SANDBOXED_AGENTS_GROUP": "live-offline", liveSummaryDirEnv: summaries})
			selected := strings.Contains(output, "live test body selected")
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if value != "1" {
				if selected || err != nil || len(calls) != 0 || !strings.Contains(output, "--- SKIP: TestLiveSuite") {
					t.Fatalf("unexpected live selection for %q: selected=%v err=%v calls=%v\n%s", value, selected, err, calls, output)
				}
				if entries, err := os.ReadDir(summaries); err != nil || len(entries) != 0 {
					t.Fatalf("skipped suite wrote %v", entries)
				}
				return
			}
			// The empty fake prerequisite answers fail the preflight, so the opted-in run fails without any mutation.
			if !selected || err == nil || len(calls) == 0 || !strings.Contains(output, "--- FAIL: TestLiveSuite/preflight") {
				t.Fatalf("opt-in: selected=%v err=%v calls=%v\n%s", selected, err, calls, output)
			}
			assertOnlyPreflightQueries(t, fakes)
		})
	}
}

// TestLiveSuiteFailsWhenPodmanIsMissingOrUnusable proves an opted-in suite fails naming Podman for a missing or failing program, after writing its failing record, through the nested test process and fake Podman.
func TestLiveSuiteFailsWhenPodmanIsMissingOrUnusable(t *testing.T) {
	for _, availability := range []string{"missing", "unusable"} {
		t.Run(availability, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			summaries := t.TempDir()
			values := map[string]string{"SANDBOXED_AGENTS_GROUP": "live-offline", liveSummaryDirEnv: summaries, "TEST_PODMAN_EXIT": "23", "TEST_PODMAN_OUTPUT": "scripted Podman failure\n"}
			if availability == "missing" {
				values["PATH"] = t.TempDir()
			}
			output, err := liveSuiteChild(t, fakes, values)
			if status := exitStatus(t, err); status != 1 || !strings.Contains(output, "--- FAIL: TestLiveSuite/preflight") || !strings.Contains(strings.ToLower(output), "podman") {
				t.Fatalf("opted-in %s Podman: status=%d output=%s; want preflight failure naming Podman", availability, status, output)
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if availability == "missing" && len(calls) != 0 {
				t.Fatalf("missing Podman calls: %v", calls)
			}
			if availability == "unusable" && (len(calls) == 0 || !strings.Contains(output, "scripted Podman failure")) {
				t.Fatalf("unusable Podman: calls=%v output=%s", calls, output)
			}
			assertOnlyPreflightQueries(t, fakes)
			if _, err := os.Stat(filepath.Join(summaries, liveSummaryFilename(t))); err != nil {
				t.Fatalf("failing record was not written before the first Podman use: %v", err)
			}
		})
	}
}

// TestLiveSuiteRefusesDefaultAndInvalidControllerGroups proves the suite exits non-zero naming SANDBOXED_AGENTS_GROUP for the default group, an unset group, and names outside ^[a-z0-9][a-z0-9-]*$, before Podman is called and without writing a record, through the nested test process and fake Podman.
func TestLiveSuiteRefusesDefaultAndInvalidControllerGroups(t *testing.T) {
	for _, group := range []string{"default", "", "Live", "live.suite", "-live", "live suite", "live_suite"} {
		t.Run(strings.Map(func(r rune) rune {
			if r == ' ' {
				return '_'
			}
			return r
		}, group), func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			summaries := t.TempDir()
			output, err := liveSuiteChild(t, fakes, map[string]string{"SANDBOXED_AGENTS_GROUP": group, liveSummaryDirEnv: summaries})
			if status := exitStatus(t, err); status != 1 || !strings.Contains(output, "SANDBOXED_AGENTS_GROUP") || !strings.Contains(output, "--- FAIL: TestLiveSuite") {
				t.Fatalf("group %q: status=%d output=%s; want refusal naming the variable", group, status, output)
			}
			assertNoExternalCalls(t, fakes)
			if entries, err := os.ReadDir(summaries); err != nil || len(entries) != 0 {
				t.Fatalf("refused run wrote %v", entries)
			}
		})
	}
}

// TestLiveSuiteRequiresSummaryDirectory proves the suite exits non-zero naming SANDBOXED_AGENTS_LIVE_SUMMARY_DIR when no record directory is given, before Podman is called, through the nested test process and fake Podman.
func TestLiveSuiteRequiresSummaryDirectory(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	output, err := liveSuiteChild(t, fakes, map[string]string{"SANDBOXED_AGENTS_GROUP": "live-offline"})
	if status := exitStatus(t, err); status != 1 || !strings.Contains(output, liveSummaryDirEnv) {
		t.Fatalf("status=%d output=%s; want refusal naming %s", status, output, liveSummaryDirEnv)
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveSuiteWritesRedactedFailingSummary proves a run that fails at the preflight leaves one record with the gate's exact fields, the checkout's commit, the host's platform, every later check as not-run, the image selection as given, no build call, and no host detail, and that the gate refuses it only for its result, through the nested test process, fake Podman, and EvaluateStableGate.
func TestLiveSuiteWritesRedactedFailingSummary(t *testing.T) {
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("nested offline guard isolates PATH to fake programs; the outer suite compares the record's commit with git")
	}
	head, err := exec.Command("git", "-C", "../..", "rev-parse", "--verify", "HEAD^{commit}").Output()
	if err != nil {
		t.Fatalf("read HEAD with git: %v", err)
	}
	commit := strings.TrimSpace(string(head))
	hostname, _ := os.Hostname()
	account, _ := user.Current()
	for _, selection := range []string{"", "0", "true", "1"} {
		t.Run("image-opt-in-"+selection, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			summaries := t.TempDir()
			const group = "live-offline-redaction"
			output, err := liveSuiteChild(t, fakes, map[string]string{"SANDBOXED_AGENTS_GROUP": group, liveSummaryDirEnv: summaries, liveImagesEnv: selection})
			if err == nil {
				t.Fatalf("offline live run passed:\n%s", output)
			}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				for _, argument := range call {
					if argument == "build" || argument == "create" {
						t.Fatalf("offline run reached a mutation: %v", call)
					}
				}
			}
			filename := liveSummaryFilename(t)
			entries, err := os.ReadDir(summaries)
			if err != nil || len(entries) != 1 || entries[0].Name() != filename {
				t.Fatalf("record directory holds %v (err=%v); want only %s", entries, err, filename)
			}
			data, err := os.ReadFile(filepath.Join(summaries, filename))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("record is not one JSON object: %v\n%s", err, data)
			}
			want := map[string]string{
				"schema_version": "2", "kind": `"live-suite"`, "commit": `"` + commit + `"`,
				"platform": `"` + strings.TrimSuffix(strings.TrimPrefix(filename, "live-suite-"), ".json") + `"`,
				"result":   `"fail"`, "image_part_selected": map[bool]string{false: "false", true: "true"}[selection == "1"],
				"image_part_ran": "false", "image_coverage_complete": "false",
				"checks": `[{"name":"preflight","result":"fail"},{"name":"lifecycle","result":"not-run"},{"name":"update","result":"not-run"},{"name":"agent","result":"not-run"},{"name":"images","result":"not-run"}]`,
			}
			got := map[string]string{}
			for key, value := range fields {
				got[key] = string(value)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("record fields=%v\nwant %v\noutput:\n%s", got, want, output)
			}
			// A host or account named like a fixed word of the record, such as a
			// user called agent, cannot be told from that word and is not a leak.
			vocabulary := strings.Join(liveCheckNames, " ") + " pass fail not-run live-suite linux windows-11"
			for _, detail := range []string{group, summaries, fakes.Dir, hostname, account.Username, "sandboxed-agents.", "/", `\`, "live-offline"} {
				if detail != "" && !strings.Contains(vocabulary, detail) && strings.Contains(string(data), detail) {
					t.Fatalf("record carries host detail %q: %s", detail, data)
				}
			}
			evidence := release.StableGateEvidence{
				Tag: "v1.0.0", Commit: commit, CICommit: commit, CIGreen: true, AssetHash: strings.Repeat("a", 64),
				Preview: &release.PreviewEvidence{Tag: "v1.0.0-preview.20261016.1", Commit: commit, AssetHash: strings.Repeat("a", 64), Records: map[string][]byte{
					"live-suite-linux.json":      []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + commit + `","platform":"linux","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[{"name":"images","result":"pass"}]}`),
					"live-suite-windows-11.json": []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + commit + `","platform":"windows-11","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[{"name":"images","result":"pass"}]}`),
					"manual-checklist.json":      []byte(`{"schema_version":1,"kind":"manual-checklist","commit":"` + commit + `","result":"pass","checks":[{"name":"github-login","result":"pass","date":"2026-10-16","platform":"linux"}]}`),
				}},
			}
			evidence.Preview.Records[filename] = data
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), filename) || !strings.Contains(err.Error(), "did not pass") {
				t.Fatalf("gate error=%v; want %s refused only for its failing result", err, filename)
			}
		})
	}
}
