package liverun

import (
	"errors"
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

// TestBuildChannelConcurrentPublication proves independent primary producers publish complete typed events without lost updates.
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
}

// TestBuildChannelEvidenceFailuresPreserveBuildOutcome proves missing/ambiguous primary evidence is supplemental and blocks cleanup authority.
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
			if readErr == nil || len(results) != 0 {
				t.Fatalf("incomplete primary evidence admitted: %v %v", results, readErr)
			}
		})
	}
}

// TestBuildChannelDisabledPreservesBuildAPI proves ordinary builds request neither iidfile nor inspection.
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
