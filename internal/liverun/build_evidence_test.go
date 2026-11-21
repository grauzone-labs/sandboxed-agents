package liverun

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// buildChannelFixture creates a real private channel outside its fixture checkout.
func buildChannelFixture(t *testing.T) (Options, Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	o := Options{Checkout: filepath.Join(root, "checkout"), RunDir: filepath.Join(root, "private"), Revision: strings.Repeat("a", 40)}
	if err := os.Mkdir(o.RunDir, 0700); err != nil {
		t.Fatal(err)
	}
	before := Snapshot{EngineID: "engine:/storage"}
	directory, err := createBuildChannel(o, before)
	if err != nil {
		t.Fatal(err)
	}
	return o, before, directory
}

// TestBuildChannelConcurrentPublication proves independent primary producers publish complete typed events with distinct attempt identities and without lost updates. It exercises the CaptureBuildResult producer and typed private artifacts seam.
func TestBuildChannelConcurrentPublication(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id := "sha256:" + strings.Repeat("b", 64)
			asset := strings.Repeat("e", 64)
			if err := CaptureBuildResult(directory, "localhost/sandboxed-agents:none-"+asset, asset, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	results, err := readBuildResults(o, before)
	if err != nil || len(results) != 12 {
		t.Fatalf("lost or partial publication: %d %v", len(results), err)
	}
	// Every worker returns the same image ID, so only distinct attempt IDs show 12 independent receipts.
	attempts := map[string]bool{}
	for _, result := range results {
		attempts[result.AttemptID] = true
	}
	if len(attempts) != 12 {
		t.Fatalf("receipts share attempt identities: %d distinct of %d", len(attempts), len(results))
	}
}

// TestBuildChannelEvidenceFailuresPreserveBuildOutcome proves missing/ambiguous primary evidence is supplemental and blocks cleanup authority. It exercises the CaptureBuildResult producer and typed private artifacts seam.
func TestBuildChannelEvidenceFailuresPreserveBuildOutcome(t *testing.T) {
	for _, kind := range []string{"missing-iid", "mismatch", "inspect-error", "publication-error", "build-error", "malformed-event"} {
		t.Run(kind, func(t *testing.T) {
			o, before, directory := buildChannelFixture(t)
			asset := strings.Repeat("e", 64)
			id := "sha256:" + strings.Repeat("b", 64)
			buildError := errors.New("actual build failure")
			err := CaptureBuildResult(directory, "localhost/sandboxed-agents:none-"+asset, asset, func(path string) error {
				if kind == "build-error" {
					return buildError
				}
				if kind == "missing-iid" {
					return nil
				}
				return os.WriteFile(path, []byte(id), 0600)
			}, func(string) (string, error) {
				switch kind {
				case "mismatch":
					return "sha256:" + strings.Repeat("d", 64), nil
				case "inspect-error":
					return "", errors.New("inspect failed")
				case "publication-error":
					if err := os.Rename(directory, directory+"-moved"); err != nil {
						t.Fatal(err)
					}
				}
				return id, nil
			})
			if kind == "build-error" {
				if !errors.Is(err, buildError) {
					t.Fatalf("actual build outcome lost: %v", err)
				}
			} else if err != nil {
				t.Fatalf("telemetry changed build result: %v", err)
			}
			if kind == "publication-error" {
				if err = os.Rename(directory+"-moved", directory); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "malformed-event" {
				if err = os.WriteFile(filepath.Join(directory, "result-forged.json"), []byte(`{"Version":1,"RawOutput":"forged"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			results, readErr := readBuildResults(o, before)
			if kind == "build-error" {
				if readErr != nil || len(results) != 0 {
					t.Fatalf("returned failed build blocked channel: %v %v", results, readErr)
				}
				return
			}
			if readErr == nil || kind != "malformed-event" && len(results) != 0 {
				t.Fatalf("incomplete primary evidence admitted: %v %v", results, readErr)
			}
		})
	}
}

// TestStrictDecodeRejectsDuplicateMembers proves the strict private-evidence decoder refuses a report that names a
// member twice, at the top level, in any letter case, or inside a nested object, and still accepts one unambiguous Removed array.
func TestStrictDecodeRejectsDuplicateMembers(t *testing.T) {
	removed := `[{"id":"sha256:` + strings.Repeat("b", 64) + `","size_bytes":1,"size_verified":true}]`
	for _, tc := range []struct {
		name, data string
		accepted   bool
	}{
		{"single", `{"status":"complete","removed":` + removed + `}`, true},
		{"duplicate", `{"status":"complete","removed":` + removed + `,"removed":[]}`, false},
		{"case-variant", `{"status":"complete","Removed":[],"removed":` + removed + `}`, false},
		{"nested", `{"status":"complete","removed":[{"id":"a","id":"b","size_bytes":1,"size_verified":true}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report CleanupReport
			err := strictDecode([]byte(tc.data), &report)
			if tc.accepted != (err == nil) {
				t.Fatalf("accepted=%v, got %v", tc.accepted, err)
			}
			if tc.accepted && (len(report.Removed) != 1 || report.Removed[0].ID != "sha256:"+strings.Repeat("b", 64)) {
				t.Fatalf("unambiguous report misread: %+v", report)
			}
		})
	}
}

// TestBuildChannelRefusesOversizedDirectory proves the private build-channel reader refuses a channel holding more than
// 10,001 entries beside nothing else as oversized and admits no result from it.
func TestBuildChannelRefusesOversizedDirectory(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	for i := 0; i < 10001; i++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("read-loss-%05d.json", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	state := readBuildChannel(o, before)
	if state.Err == nil || state.Err.Error() != "build channel unreadable or oversized" || len(state.Results) != 0 {
		t.Fatalf("oversized channel admitted: %+v", state)
	}
}

// TestBuildChannelDisabledPreservesBuildAPI proves ordinary builds request neither iidfile nor inspection. It exercises the CaptureBuildResult producer and typed private artifacts seam.
func TestBuildChannelDisabledPreservesBuildAPI(t *testing.T) {
	inspected := false
	err := CaptureBuildResult("", "unused", "", func(path string) error {
		if path != "" {
			t.Error("ordinary build received telemetry flags")
		}
		return nil
	}, func(string) (string, error) { inspected = true; return "", nil })
	if err != nil || inspected {
		t.Fatalf("ordinary build changed: %v inspect=%v", err, inspected)
	}
}

// TestSingleCommitAttemptsHaveDistinctIdentity proves separate failed producers retain unique typed attempts at the private artifact seam.
func TestSingleCommitAttemptsHaveDistinctIdentity(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	asset := strings.Repeat("e", 64)
	for _, kind := range []string{"none", "dotnet"} {
		err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:"+kind+"-"+asset, asset, false, func(string) error { return fmt.Errorf("actual build failure") }, func(string) (string, error) { t.Fatal("missing iid inspected"); return "", nil })
		if err == nil {
			t.Fatal("failure lost")
		}
	}
	paths, err := filepath.Glob(filepath.Join(directory, "attempt-*.json"))
	state := readBuildChannel(o, before)
	if err != nil || len(paths) != 2 || !state.Open || state.Err != nil {
		t.Fatalf("attempt identity lost: %v %+v", paths, state)
	}
}
