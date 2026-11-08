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
