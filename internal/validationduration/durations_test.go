package validationduration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

const record = `{"schema_version":2,"kind":"live-suite","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","result":"fail","image_part_selected":false,"image_part_ran":false,"image_coverage_complete":false,"checks":[{"name":"preflight","result":"pass"}]}`

// TestCollectRetainsOnlyInclusiveCases proves fixture pass/fail/skip durations and
// comparison context survive the verbose-log seam without nested sums or host details.
func TestCollectRetainsOnlyInclusiveCases(t *testing.T) {
	log, err := os.Open("testdata/cases.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	data, err := validationduration.Collect(log, []byte(record), validationduration.Context{HostLoad: "idle", BaseImage: "present", BuildCache: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"kind":"live-suite-durations","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","image_part_selected":false,"image_part_ran":false,"image_coverage_complete":false,"context":{"host_load":"idle","base_image":"present","build_cache":"unknown"},"cases":[{"name":"preflight","result":"pass","seconds":2.52},{"name":"lifecycle","result":"fail","seconds":34.99},{"name":"images","result":"skip","seconds":0}]}`
	var gotObject, wantObject any
	if err := json.Unmarshal(data, &gotObject); err != nil {
		t.Fatalf("evidence: %v; %s", err, data)
	}
	if err := json.Unmarshal([]byte(want), &wantObject); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotObject, wantObject) {
		t.Fatalf("got %s; want %s", data, want)
	}
}

// TestCollectRefusesUntrustedContext proves only categorical state and validated
// revision/coverage facts can cross the supplemental evidence seam.
func TestCollectRefusesUntrustedContext(t *testing.T) {
	for _, field := range []string{"host", "image", "cache", "commit", "platform", "schema", "coverage", "missing-coverage"} {
		t.Run(field, func(t *testing.T) {
			state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}
			source := record
			switch field {
			case "host":
				state.HostLoad = "private-account"
			case "image":
				state.BaseImage = "sandboxed-agents.private"
			case "cache":
				state.BuildCache = "/private/path"
			case "commit":
				source = strings.Replace(source, "0123456789abcdef0123456789abcdef01234567", "private-group", 1)
			case "platform":
				source = strings.Replace(source, `"linux"`, `"private-host"`, 1)
			case "schema":
				source = strings.Replace(source, `"schema_version":2`, `"schema_version":1`, 1)
			case "coverage":
				source = strings.Replace(source, `"image_part_ran":false`, `"image_part_ran":null`, 1)
			case "missing-coverage":
				source = strings.Replace(source, `"image_part_ran":false,`, "", 1)
			}
			data, err := validationduration.Collect(strings.NewReader(""), []byte(source), state)
			if err == nil || len(data) != 0 {
				t.Fatalf("retained invalid %s: %s; %v", field, data, err)
			}
		})
	}
}

// TestRetainPreservesFailingRecord proves supplemental collection beside an
// interrupted run's initial failing record preserves its bytes and exact schema.
func TestRetainPreservesFailingRecord(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "live-suite-linux.json")
	if err := os.WriteFile(path, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := validationduration.Retain(strings.NewReader("    --- PASS: TestLiveSuite/preflight (1.00s)\n"), path, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if output != filepath.Join(directory, "durations-linux.json") {
		t.Fatalf("output=%s", output)
	}
	if data, err := os.ReadFile(output); err != nil || !strings.Contains(string(data), `"seconds":1`) {
		t.Fatalf("evidence=%s err=%v", data, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != record {
		t.Fatalf("release record changed: %s; %v", data, err)
	}
}

// TestCollectRejectsRepeatedRuns proves concatenated verbose runs cannot silently
// double-count a case at the collector seam, while incomplete logs stay partial.
func TestCollectRejectsRepeatedRuns(t *testing.T) {
	state := validationduration.Context{HostLoad: "unknown", BaseImage: "absent", BuildCache: "cold"}
	line := "    --- PASS: TestLiveSuite/preflight (1.00s)\r\n"
	if _, err := validationduration.Collect(strings.NewReader(line+line), []byte(record), state); err == nil {
		t.Fatal("repeated case accepted")
	}
	if data, err := validationduration.Collect(strings.NewReader(line), []byte(record), state); err != nil || !strings.Contains(string(data), `"seconds":1`) {
		t.Fatalf("partial CRLF log: %s; %v", data, err)
	}
}

// TestCollectSkipsOversizedProgramLines proves valid case durations on both sides
// of untrusted oversized lines survive the collector seam without leaking output.
func TestCollectSkipsOversizedProgramLines(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097, 65535, 65536, 65537, 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			before := "    --- PASS: TestLiveSuite/preflight (1.00s)\n"
			after := "    --- FAIL: TestLiveSuite/lifecycle (2.00s)\r\n"
			// A completion-shaped tail must not become a new line when a chunk is skipped.
			untrusted := strings.Repeat("private", size/7+1)[:size] + after
			state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}
			const wantCases = `[{"name":"preflight","result":"pass","seconds":1},{"name":"lifecycle","result":"fail","seconds":2}]`
			for _, log := range []string{before + untrusted + after, before + after + strings.TrimSuffix(untrusted, "\r\n")} {
				got, err := validationduration.Collect(strings.NewReader(log), []byte(record), state)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(got, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["cases"]) != wantCases || strings.Contains(string(got), "private") {
					t.Fatalf("oversized output: got %s; want cases %s without program output", got, wantCases)
				}
			}
		})
	}
}

// failingLogReader returns a stable error at the untrusted log read boundary.
type failingLogReader struct{ err error }

// Read reports the injected source failure without supplying log bytes.
func (r failingLogReader) Read([]byte) (int, error) { return 0, r.err }

// TestCollectPreservesReadErrors proves source failures, including after an
// oversized line fragment, are returned without publishing partial evidence.
func TestCollectPreservesReadErrors(t *testing.T) {
	failure := errors.New("log source failed")
	for _, prefix := range []string{"", "    --- PASS: TestLiveSuite/preflight (1.00s)\n", strings.Repeat("x", 65537)} {
		log := io.MultiReader(strings.NewReader(prefix), failingLogReader{failure})
		data, err := validationduration.Collect(log, []byte(record), validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"})
		if !errors.Is(err, failure) || len(data) != 0 {
			t.Fatalf("data=%s error=%v; want original read failure", data, err)
		}
	}
}
