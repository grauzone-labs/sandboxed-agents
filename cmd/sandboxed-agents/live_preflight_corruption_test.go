package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveTimingNumericCorruptionPreservesSuite proves overflowing journal data
// cannot fail a healthy suite or lose T/core operations, while a good row remains
// measured and the invalid files stay counted and named after both the suite's
// write and Retain, through a nested source-built CLI, actual final cleanup and Retain.
func TestLiveTimingNumericCorruptionPreservesSuite(t *testing.T) {
	root := t.TempDir()
	output, err := timingChild(t, "TestLiveTimingNumericFixture", root)
	if err != nil {
		t.Fatalf("optional timing changed the suite verdict: %v\n%s", err, output)
	}
	directory := filepath.Join(root, "records")
	// Both the suite's final write and Retain's rewrite must keep T, the core
	// operations, the good row and the invalid-file evidence.
	assertEvidence := func(stage string) {
		t.Helper()
		evidence := readTimingOperations(t, directory)
		if evidence.SuiteSeconds == nil || len(evidence.Operations) != 3 || evidence.Preflight == nil || evidence.Preflight.State != "incomplete" || evidence.Preflight.Preflight.Measured != 1 || evidence.Preflight.Preflight.Seconds == nil || *evidence.Preflight.Preflight.Seconds != 0.25 || evidence.Preflight.InvalidFiles != 2 || !slices.Contains(evidence.Preflight.Reasons, "invalid-file") {
			t.Fatalf("%s lost independent evidence, the good observation or invalid-file evidence: %+v", stage, evidence)
		}
	}
	assertEvidence("suite")
	record := filepath.Join(directory, "live-suite-"+livePlatform()+".json")
	before, err := os.ReadFile(record)
	if err != nil || !bytes.Contains(before, []byte(`"result":"pass"`)) {
		t.Fatalf("suite record: %v %s", err, before)
	}
	if _, err := validationduration.Retain(bytes.NewReader(output), record, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", PreflightDir: filepath.Join(root, "preflight-timing")}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(record)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("collector changed schema2")
	}
	assertEvidence("collector")
}

// timingChild starts one bounded nested fixture, allowing its TestMain to build
// the actual checkout source with its Go toolchain available even under the
// offline guard; module fetching is disabled before the fixture isolates PATH.
func timingChild(t *testing.T, name, root string) ([]byte, error) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-test.run=^"+name+"$", "-test.v", "-test.timeout=90s")
	child.Env = testutil.Env(os.Environ(), map[string]string{"PATH": filepath.Join(runtime.GOROOT(), "bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "GOPROXY": "off", "GOSUMDB": "off", "TEST_TIMING_FIXTURE_ROOT": root, "TEST_SANDBOXED_AGENTS_EXECUTABLE": "", "TEST_LIVE_PREPARED_EXECUTABLE": "", "TEST_FAKE_EXECUTABLE": "", "TEST_RELEASE_EXECUTABLE": "", testutil.LiveTestsEnv: "", "SANDBOXED_AGENTS_LIVE_IMAGES_TESTS": "", preflighttiming.DirEnv: "", preflighttiming.TokenEnv: ""})
	return child.CombinedOutput()
}

// timingFixture isolates all host lookup/state before constructing a suite;
// a fixture flag without its parent directory cannot reach cleanup or an engine.
func timingFixture(t *testing.T) (*liveSuite, string, testutil.Fakes) {
	t.Helper()
	root := os.Getenv("TEST_TIMING_FIXTURE_ROOT")
	if root == "" {
		t.Skip("nested timing fixture requires its parent's private directory")
	}
	info, err := os.Lstat(root)
	if !filepath.IsAbs(root) || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("invalid timing fixture directory")
	}
	fakes := testutil.Install(t, fakeExecutable)
	// Install every fake/environment boundary before any CLI command or cleanup.
	for _, entry := range fakes.Env(os.Environ()) {
		key, value, _ := strings.Cut(entry, "=")
		if slices.Contains([]string{"PATH", "XDG_STATE_HOME", "LOCALAPPDATA", "TEST_PODMAN_LOG", "TEST_SSH_LOG", "TEST_PODMAN_RESPONSES", "TEST_SSH_RESPONSES", "TEST_PODMAN_OUTPUT", "TEST_PODMAN_EXIT", "TEST_PODMAN_ENV_LOG", "TEST_SSH_OUTPUT", "TEST_SSH_EXIT"}, key) {
			t.Setenv(key, value)
		}
	}
	t.Setenv("TEST_SSH_REAL_EXECUTABLE", "")
	t.Setenv("TEST_AZURE_REMOTE_DIR", "")
	t.Setenv("HOME", fakes.Dir)
	t.Setenv("USERPROFILE", fakes.Dir)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "offline-timing-active")
	t.Setenv(liveRecordDirEnv, filepath.Join(root, "records"))
	t.Setenv(liverun.CaseOverlapEnv, "2")
	checkout, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := liveCommit(checkout)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := preflighttiming.Create(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(preflighttiming.DirEnv, channel)
	suite := newLiveSuite(t)
	if suite.host.ledger == nil {
		t.Fatal("actual source fixture lost its active ledger")
	}
	return suite, root, fakes
}

// TestLiveTimingNumericFixture injects finite-but-overflowing private observations
// after three actual source-built version calls and exercises final suite cleanup.
func TestLiveTimingNumericFixture(t *testing.T) {
	suite, root, fakes := timingFixture(t)
	for range 3 {
		suite.host.mustCLI(t, "", "version")
	}
	journal, err := preflighttiming.Read(filepath.Join(root, "preflight-timing"), suite.commit)
	if err != nil || len(journal.Entries) != 3 {
		t.Fatalf("source-child setup: %+v %v", journal, err)
	}
	for index, entry := range journal.Entries {
		seconds := 1e308
		if index == 2 {
			seconds = 0.25
		}
		entry.Invocation.Preflight = &preflighttiming.Phase{State: "closed", Outcome: "pass", Seconds: &seconds}
		data, err := json.Marshal(entry.Invocation)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "preflight-timing", "inv-"+entry.Invocation.Token+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveTimingNestedFixtureWithIsolatedParentPATH proves a nested offline
// caller's fake-only PATH still rebuilds actual source without executable
// overrides, through the fixture process boundary used by the offline guard.
func TestLiveTimingNestedFixtureWithIsolatedParentPATH(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	output, err := timingChild(t, "TestLiveTimingNumericFixture", t.TempDir())
	if err != nil {
		t.Fatalf("source build under isolated parent PATH: %v\n%s", err, output)
	}
	assertNoExternalCalls(t, fakes)
}
