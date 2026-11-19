package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunRetainsStdinWithUnknownContext proves the offline command writes its
// platform supplemental file with unknown defaults without changing the input record.
func TestRunRetainsStdinWithUnknownContext(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "live-suite-windows-11.json")
	const source = `{"schema_version":2,"kind":"live-suite","commit":"0123456789abcdef0123456789abcdef01234567","platform":"windows-11","result":"fail","image_part_selected":true,"image_part_ran":false,"image_coverage_complete":false,"checks":[]}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := run([]string{"-record", path}, strings.NewReader("    --- FAIL: TestLiveSuite/preflight (19.58s)\r\n"), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "durations-windows-11.json")
	if output.String() != destination+"\n" {
		t.Fatalf("output=%q", output.String())
	}
	data, err := os.ReadFile(destination)
	if err != nil || !strings.Contains(string(data), `"host_load":"unknown","base_image":"unknown","build_cache":"unknown"`) || !strings.Contains(string(data), `"seconds":19.58`) {
		t.Fatalf("evidence=%s error=%v", data, err)
	}
	if err := run([]string{"-record", path, "-host-load", "private-account"}, strings.NewReader(""), &output, &diagnostics); err == nil {
		t.Fatal("arbitrary host context accepted")
	}
	if after, err := os.ReadFile(destination); err != nil || !bytes.Equal(after, data) {
		t.Fatal("failed collection changed previous evidence")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("collection changed schema 2 record")
	}
}

// TestRunRetainsPreparationAndPostSuiteSeconds proves timestamp inputs become
// elapsed seconds, without retaining timestamps, through the offline command seam.
func TestRunRetainsPreparationAndPostSuiteSeconds(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "live-suite-linux.json")
	const source = `{"schema_version":2,"kind":"live-suite","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","result":"fail","image_part_selected":false,"image_part_ran":false,"image_coverage_complete":false,"checks":[]}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	args := []string{"-record", path, "-preparation-start", "2026-11-18T08:00:00Z", "-preparation-end", "2026-11-18T08:00:37.5Z", "-post-suite-start", "2026-11-18T09:00:00Z", "-post-suite-end", "2026-11-18T09:01:02Z"}
	if err := run(args, strings.NewReader(""), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "durations-linux.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"preparation_seconds":37.5`) || !strings.Contains(string(data), `"post_suite_seconds":62`) || strings.Contains(string(data), "2026-") {
		t.Fatalf("phase evidence: %s", data)
	}
}

// TestRunReportsRetainedDurationsWhenOptionalEvidenceIsInvalid proves the command
// returns the independent durations path and an explicit optional-evidence error.
func TestRunReportsRetainedDurationsWhenOptionalEvidenceIsInvalid(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "live-suite-linux.json")
	const source = `{"schema_version":2,"kind":"live-suite","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","result":"fail","image_part_selected":false,"image_part_ran":false,"image_coverage_complete":false,"checks":[]}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "operations-linux.json"), []byte(`{"partial":`), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	err := run([]string{"-record", path}, strings.NewReader("    --- FAIL: TestLiveSuite/preflight (19.58s)\n"), &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "optional operation evidence") || output.String() != filepath.Join(directory, "durations-linux.json")+"\n" {
		t.Fatalf("output=%q err=%v", output.String(), err)
	}
}
