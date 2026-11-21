package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestTransientManifestReadLossCannotCompleteCoverage proves manifest disappearance or invalidation and recovery cannot hide an unreceipted build at the public producer/filesystem-to-Cleanup seam.
func TestTransientManifestReadLossCannotCompleteCoverage(t *testing.T) {
	for _, mode := range []string{"capture", "observation", "invalid-content", "entire-channel"} {
		t.Run(mode, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			id := "sha256:" + strings.Repeat("b", 64)
			unknown := "sha256:" + strings.Repeat("c", 64)
			asset := strings.Repeat("e", 64)
			tag := "localhost/sandboxed-agents:none-" + asset
			if err := liverun.CaptureSingleCommitBuild(directory, tag, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(directory, "manifest.json")
			held := filepath.Join(options.RunDir, "manifest-held")
			if mode == "entire-channel" {
				manifest = directory
				held = filepath.Join(options.RunDir, "channel-held")
			}
			if err := os.Rename(manifest, held); err != nil {
				t.Fatal(err)
			}
			if mode == "invalid-content" {
				if err := os.WriteFile(manifest, []byte(`{"Version":999}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			failure := errors.New("original build failure")
			if mode == "capture" {
				calls := 0
				err := liverun.CaptureSingleCommitBuild(directory, tag, asset, false, func(path string) error {
					calls++
					if path != "" {
						t.Fatal("missing manifest supplied iidfile")
					}
					return failure
				}, func(string) (string, error) { t.Fatal("unconfirmed build inspected"); return "", nil })
				if err != failure || calls != 1 {
					t.Fatalf("build outcome changed: %v calls=%d", err, calls)
				}
			} else if err := liverun.RecordUnconfirmedBuild(directory); err == nil {
				t.Fatal("unreadable manifest accepted")
			}
			if mode == "invalid-content" {
				if err := os.Remove(manifest); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(held, manifest); err != nil {
				t.Fatal(err)
			}
			leftover := cleanupImageFixture(unknown)
			independent := cleanupImageFixture(id)
			want := "foreign or mixed image tags"
			if mode == "capture" {
				// The layer-free final keeps its asset label but another workflow retags it after commit.
				leftover.RepoTags = []string{"private.example/retagged:current"}
				independent.RepoTags = nil
			} else {
				// Unconfirmed engine selection uses the original default layers and can leave an unlabelled intermediate.
				leftover.RepoTags = nil
				leftover.Labels = nil
				want = "missing exact build proof"
			}
			fake.images = map[string]cleanupFakeImage{id: independent, unknown: leftover}
			var out bytes.Buffer
			_ = svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("no audit: %v %s", err, &out)
			}
			removed := cleanupRemoved(fake)
			wantStatus := "unverified"
			if mode == "entire-channel" {
				wantStatus = "partial"
			}
			if report.Status != wantStatus || len(removed) != 1 || removed[0] != id {
				t.Fatalf("transient read loss hidden or independent proof lost: removed=%v %s", removed, &out)
			}
			if len(report.Skipped) != 1 || report.Skipped[0].ID != unknown || report.Skipped[0].Reason != want {
				t.Fatalf("unreceipted image falsely excluded: %s", &out)
			}
			if strings.Contains(out.String(), "private.example") {
				t.Fatal("foreign host tag leaked")
			}
			if strings.Contains(out.String(), "not committed by this run's builds") {
				t.Fatalf("lost evidence falsely established nonownership: %s", &out)
			}
			if _, err := os.Stat(filepath.Join(options.RunDir, "build-evidence-unverified")); !os.IsNotExist(err) {
				t.Fatalf("unreadable manifest wrote outside channel: %v", err)
			}
		})
	}
}

// TestUnconfirmedPublicationFailureKeepsIndependentProof proves a genuinely unwritable channel falls back to the run-private marker while production Cleanup retains independent authority at the filesystem/journal seam.
func TestUnconfirmedPublicationFailureKeepsIndependentProof(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	svc.BuildEvidence = nil
	directory := filepath.Join(options.RunDir, "build-evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
	id := "sha256:" + strings.Repeat("b", 64)
	asset := strings.Repeat("e", 64)
	if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0700); err != nil {
			t.Error(err)
		}
	})
	probePath := filepath.Join(directory, "publication-probe")
	probe, err := os.OpenFile(probePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_ = probe.Close()
		if err = os.Remove(probePath); err != nil {
			t.Fatal(err)
		}
		t.Skip("verified fixture prerequisite absent: directory mode does not deny writes to this process")
	}
	if !os.IsPermission(err) {
		t.Fatalf("unexpected publication fixture failure: %v", err)
	}
	if err = liverun.RecordUnconfirmedBuild(directory); !os.IsPermission(err) {
		t.Fatalf("publication did not reach the denied write: %v", err)
	}
	if err = os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(options.RunDir, "build-evidence-unverified"))
	if err != nil || string(marker) != "producer evidence unavailable\n" {
		t.Fatalf("missing fallback marker: %q %v", marker, err)
	}
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	var out bytes.Buffer
	_ = svc.Cleanup(context.Background(), options, &out)
	var report liverun.CleanupReport
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("no audit: %v %s", err, &out)
	}
	removed := cleanupRemoved(fake)
	if report.Status != "unverified" || len(report.Unverified) == 0 || len(removed) != 1 || removed[0] != id {
		t.Fatalf("fallback discarded independent proof: removed=%v %s", removed, &out)
	}
}
