package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

func passingLiveRecord(platform string) release.LiveSuiteRecord {
	return release.LiveSuiteRecord{
		Commit: gateCommit, Platform: platform, Result: "pass",
		ImagePartSelected: true, ImagePartRan: true, ImageCoverageComplete: true,
		Checks: []release.LiveSuiteCheck{{Name: "preflight", Result: "pass"}, {Name: "images", Result: "pass"}},
	}
}

// TestLiveSuiteRecordRoundTripsThroughStableGate proves a written passing record for each platform is accepted, and a failing or image-less one refused by name, through WriteLiveSuiteRecord and EvaluateStableGate.
func TestLiveSuiteRecordRoundTripsThroughStableGate(t *testing.T) {
	directory := t.TempDir()
	evidence := validatedEvidence()
	for _, platform := range []string{"linux", "windows-11"} {
		path, err := release.WriteLiveSuiteRecord(directory, passingLiveRecord(platform))
		if err != nil || filepath.Dir(path) != directory || filepath.Base(path) != "live-suite-"+platform+".json" {
			t.Fatalf("write %s: path=%q err=%v", platform, path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		evidence.Preview.Records[filepath.Base(path)] = data
	}
	if err := release.EvaluateStableGate(evidence); err != nil {
		t.Fatalf("gate refused written records: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*release.LiveSuiteRecord)
		want   string
	}{
		{"failing run", func(r *release.LiveSuiteRecord) { r.Result = "fail" }, "did not pass"},
		{"image part not run", func(r *release.LiveSuiteRecord) { r.ImagePartRan, r.ImageCoverageComplete = false, false }, "image coverage"},
		{"image cleanup incomplete", func(r *release.LiveSuiteRecord) { r.ImageCoverageComplete = false }, "image coverage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := passingLiveRecord("linux")
			tc.change(&record)
			data, err := release.EncodeLiveSuiteRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			evidence := validatedEvidence()
			evidence.Preview.Records["live-suite-linux.json"] = data
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "live-suite-linux.json") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v; want live-suite-linux.json refusal naming %q", err, tc.want)
			}
		})
	}
}

// TestLiveSuiteRecordHoldsOnlyFixedFields proves the encoded object carries exactly the gate's fields with schema 2 and kind live-suite, and each check only name and result, through EncodeLiveSuiteRecord.
func TestLiveSuiteRecordHoldsOnlyFixedFields(t *testing.T) {
	record := passingLiveRecord("windows-11")
	record.Checks = append(record.Checks, release.LiveSuiteCheck{Name: "agent", Result: "not-run"})
	data, err := release.EncodeLiveSuiteRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	want := map[string]string{
		"schema_version": "2", "kind": `"live-suite"`, "commit": `"` + gateCommit + `"`, "platform": `"windows-11"`, "result": `"pass"`,
		"image_part_selected": "true", "image_part_ran": "true", "image_coverage_complete": "true",
		"checks": `[{"name":"preflight","result":"pass"},{"name":"images","result":"pass"},{"name":"agent","result":"not-run"}]`,
	}
	got := map[string]string{}
	for key, value := range fields {
		got[key] = string(value)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fields=%v\nwant %v", got, want)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("record does not end with one newline: %q", data)
	}
}

// TestLiveSuiteRecordReplacesFileAtomically proves a second write replaces the previous record in place and leaves no temporary file behind through WriteLiveSuiteRecord.
func TestLiveSuiteRecordReplacesFileAtomically(t *testing.T) {
	directory := t.TempDir()
	failing := passingLiveRecord("linux")
	failing.Result, failing.ImagePartRan, failing.ImageCoverageComplete = "fail", false, false
	failing.Checks = []release.LiveSuiteCheck{{Name: "preflight", Result: "not-run"}}
	if _, err := release.WriteLiveSuiteRecord(directory, failing); err != nil {
		t.Fatal(err)
	}
	if _, err := release.WriteLiveSuiteRecord(directory, passingLiveRecord("linux")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "live-suite-linux.json" {
		t.Fatalf("directory holds %v; want only the record", entries)
	}
	data, err := os.ReadFile(filepath.Join(directory, "live-suite-linux.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"result":"pass"`) || strings.Contains(string(data), "not-run") {
		t.Fatalf("second write did not replace the record: %s", data)
	}
}

// TestLiveSuiteRecordRefusesInvalidValues proves a record outside the gate's vocabulary is refused before any file is written through WriteLiveSuiteRecord.
func TestLiveSuiteRecordRefusesInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*release.LiveSuiteRecord)
	}{
		{"short commit", func(r *release.LiveSuiteRecord) { r.Commit = "abc123" }},
		{"uppercase commit", func(r *release.LiveSuiteRecord) { r.Commit = strings.ToUpper(gateCommit) }},
		{"platform", func(r *release.LiveSuiteRecord) { r.Platform = "windows" }},
		{"result", func(r *release.LiveSuiteRecord) { r.Result = "passed" }},
		{"check result", func(r *release.LiveSuiteRecord) { r.Checks[0].Result = "skipped" }},
		{"empty check name", func(r *release.LiveSuiteRecord) { r.Checks[0].Name = "" }},
		{"repeated check", func(r *release.LiveSuiteRecord) { r.Checks = append(r.Checks, r.Checks[0]) }},
		{"no checks", func(r *release.LiveSuiteRecord) { r.Checks = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			record := passingLiveRecord("linux")
			tc.change(&record)
			if _, err := release.WriteLiveSuiteRecord(directory, record); err == nil {
				t.Fatal("invalid record was written")
			}
			if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
				t.Fatalf("directory entries=%v err=%v; want none", entries, err)
			}
		})
	}
	if _, err := release.LiveSuiteRecordFilename("macos"); err == nil {
		t.Fatal("unknown platform got a record filename")
	}
}
