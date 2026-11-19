package liverun

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSingleCommitReceiptPreservesFailedBuild proves the producer/artifact seam retains a confirmed v2 result even when the original build returns an error.
func TestSingleCommitReceiptPreservesFailedBuild(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	asset := strings.Repeat("e", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	failure := errors.New("actual build failed after commit")
	err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error {
		if err := os.WriteFile(path, []byte(id), 0600); err != nil {
			t.Fatal(err)
		}
		return failure
	}, func(string) (string, error) { return id, nil })
	results, readErr := readBuildResults(o, before)
	if !errors.Is(err, failure) || readErr != nil || len(results) != 1 || results[0].ID != id || results[0].Policy != "single-commit" || results[0].Outcome != "committed-build-error" {
		t.Fatalf("confirmed failed final lost: err=%v read=%v results=%v", err, readErr, results)
	}
}

// TestUnconfirmedPublicationPreservesConcurrentFinals proves the public producers atomically retain every observation and independent final at the private journal seam under concurrent publication.
func TestUnconfirmedPublicationPreservesConcurrentFinals(t *testing.T) {
	o, before, directory := buildChannelFixture(t)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(2)
		go func() {
			defer workers.Done()
			if err := RecordUnconfirmedBuild(directory); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer workers.Done()
			id := "sha256:" + strings.Repeat("b", 64)
			asset := strings.Repeat("e", 64)
			if err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	state := readBuildChannel(o, before)
	observations, _ := filepath.Glob(filepath.Join(directory, "unconfirmed-*.json"))
	if len(observations) != 8 || len(state.Results) != 8 || state.Complete || !state.Open || state.Err == nil {
		t.Fatalf("lost publication or false complete: observations=%d %+v", len(observations), state)
	}
}

// TestUnconfirmedPublicationRequiresManifest proves invalid contents yield only empty in-channel negative evidence and no bound observation or parent write at the public producer/filesystem seam.
func TestUnconfirmedPublicationRequiresManifest(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "build-evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(`{"Version":999}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordUnconfirmedBuild(directory); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("missing negative evidence or extra authority: %v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Name() == "manifest.json" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !strings.HasPrefix(entry.Name(), "invalid-manifest-") || !info.Mode().IsRegular() || info.Size() != 0 {
			t.Fatalf("invalid manifest authorized a payload: %v %v", entry, err)
		}
	}
	if _, err = os.Stat(filepath.Join(root, "build-evidence-unverified")); !os.IsNotExist(err) {
		t.Fatalf("untrusted parent was written: %v", err)
	}
}

// TestInvalidCapturePreconditionsStayUnverified proves the public producer/artifact seam records failed preconditions without adding flags, inspecting, or changing the original build error.
func TestInvalidCapturePreconditionsStayUnverified(t *testing.T) {
	for _, kind := range []string{"tag", "asset", "tag-asset-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			o, before, directory := buildChannelFixture(t)
			asset := strings.Repeat("e", 64)
			tag := "localhost/sandboxed-agents:none-" + asset
			switch kind {
			case "tag":
				tag = "private.example/foreign:tag"
			case "asset":
				asset = "bad"
			case "tag-asset-mismatch":
				asset = strings.Repeat("d", 64)
			}
			failure := errors.New("original build error")
			calls := 0
			err := CaptureSingleCommitBuild(directory, tag, asset, false, func(path string) error {
				calls++
				if path != "" {
					t.Fatalf("bad capture requested iidfile: %s", path)
				}
				return failure
			}, func(string) (string, error) { t.Fatal("bad capture inspected"); return "", nil })
			state := readBuildChannel(o, before)
			if err != failure || calls != 1 || state.Err == nil || state.Complete || !state.Open || len(state.Results) != 0 {
				t.Fatalf("bad capture falsely closed or changed outcome: %v calls=%d %+v", err, calls, state)
			}
			paths, _ := filepath.Glob(filepath.Join(directory, "unconfirmed-*.json"))
			if len(paths) != 1 {
				t.Fatalf("no typed observation: %v", paths)
			}
			var event buildPublication
			manifest, manifestErr := readBuildManifest(directory)
			if err := strictBuildJSON(paths[0], &event); err != nil || manifestErr != nil || event.Invocation != manifest.Invocation || event.Revision != o.Revision || event.EngineID != before.EngineID || !event.Unconfirmed || event.Result != (BuildResult{}) || event.Input != nil {
				t.Fatalf("unbound observation: %v %+v", err, event)
			}
		})
	}
}

// TestSingleCommitUnconfirmedResultsStayOpen proves missing, mismatched and unpublished results cannot close a typed attempt at the producer/artifact seam or change the build outcome.
func TestSingleCommitUnconfirmedResultsStayOpen(t *testing.T) {
	for _, kind := range []string{"missing-iid", "mismatch", "inspect-error", "publication-error"} {
		t.Run(kind, func(t *testing.T) {
			o, before, directory := buildChannelFixture(t)
			id := "sha256:" + strings.Repeat("b", 64)
			asset := strings.Repeat("e", 64)
			err := CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error {
				if kind == "missing-iid" {
					return nil
				}
				return os.WriteFile(path, []byte(id), 0600)
			}, func(string) (string, error) {
				switch kind {
				case "mismatch":
					return "sha256:" + strings.Repeat("c", 64), nil
				case "inspect-error":
					return "", errors.New("private diagnostic")
				case "publication-error":
					if err := os.Rename(directory, directory+"-moved"); err != nil {
						t.Fatal(err)
					}
				}
				return id, nil
			})
			if kind == "publication-error" {
				if err := os.Rename(directory+"-moved", directory); err != nil {
					t.Fatal(err)
				}
			}
			state := readBuildChannel(o, before)
			if err != nil || !state.Open || len(state.Results) != 0 {
				t.Fatalf("unconfirmed result admitted or outcome changed: %v %+v", err, state)
			}
		})
	}
}
