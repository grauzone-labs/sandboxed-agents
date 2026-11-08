package main_test

import (
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

// TestLiveDurationInterruptedFixture proves the suite initialization seam writes
// its failing record before interruption, as a child of the retention test.
func TestLiveDurationInterruptedFixture(t *testing.T) {
	marker := os.Getenv("TEST_DURATION_INTERRUPTION_MARKER")
	if marker == "" {
		t.Skip("child fixture selected by the retention test")
	}
	newLiveSuite(t)
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
	t.Fatal("fixture was not interrupted")
}

// TestLiveDurationRetentionAfterInterruption proves a killed suite initializer
// leaves schema 2 failing and collecting supplemental evidence cannot pass the release gate.
func TestLiveDurationRetentionAfterInterruption(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("live-suite records require Linux or Windows")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "ready")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestLiveDurationInterruptedFixture$")
	values := nestedExecutables()
	values["TEST_DURATION_INTERRUPTION_MARKER"] = marker
	values[liveRecordDirEnv] = directory
	values["SANDBOXED_AGENTS_GROUP"] = "offline-duration"
	// The killed child cannot run TempDir cleanup; keep its files under the parent.
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		values[key] = directory
	}
	child.Env = testutil.Env(os.Environ(), values)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("suite initializer did not write its record")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("interrupted child passed")
	}
	path := filepath.Join(directory, liveRecordFilename(t))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(before, &fields); err != nil {
		t.Fatal(err)
	}
	// Schema 2 has schema_version, kind, commit, platform, result, three image
	// coverage flags, and checks; supplemental timing must add no tenth field.
	if len(fields) != 9 || string(fields["schema_version"]) != "2" || string(fields["result"]) != `"fail"` {
		t.Fatalf("interruption record: %s", before)
	}
	supplemental, err := validationduration.Retain(strings.NewReader("    --- PASS: TestLiveSuite/preflight (1.00s)\n"), path, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("retention changed release record: %v", err)
	}
	timing, err := os.ReadFile(supplemental)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Trim(string(fields["commit"]), `"`)
	records := map[string][]byte{}
	for _, platform := range []string{"linux", "windows-11"} {
		records["live-suite-"+platform+".json"] = []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + commit + `","platform":"` + platform + `","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[]}`)
	}
	records["manual-checklist.json"] = []byte(`{"schema_version":1,"kind":"manual-checklist","commit":"` + commit + `","result":"pass","checks":[{"name":"login","result":"pass","date":"2026-11-08","platform":"linux"}]}`)
	evidence := release.StableGateEvidence{Tag: "v1.0.0", Commit: commit, Preview: &release.PreviewEvidence{Tag: "v1.0.0-preview.20261108.1", Commit: commit, Records: records, AssetHash: strings.Repeat("a", 64)}, CICommit: commit, CIGreen: true, AssetHash: strings.Repeat("a", 64)}
	if err := release.EvaluateStableGate(evidence); err != nil {
		t.Fatalf("baseline gate: %v", err)
	}
	records[filepath.Base(supplemental)] = timing
	if err := release.EvaluateStableGate(evidence); err != nil {
		t.Fatalf("supplemental file affected passing gate: %v", err)
	}
	records[liveRecordFilename(t)] = after
	if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("gate accepted interrupted run: %v", err)
	}
	records[liveRecordFilename(t)] = timing
	if err := release.EvaluateStableGate(evidence); err == nil {
		t.Fatal("supplemental evidence replaced required gate record")
	}
}
