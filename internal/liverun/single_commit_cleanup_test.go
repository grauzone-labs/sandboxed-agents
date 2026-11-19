package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestSingleCommitCoverage proves the producer-to-Cleanup seam separates closed commits, open failures, retained registry inputs, reproducible reuse and legacy cacheable results.
func TestSingleCommitCoverage(t *testing.T) {
	for _, mode := range []string{"closed", "failed-final", "open", "open-empty", "open-pull", "reuse", "legacy", "conflicting", "mixed-reuse", "repeated-default", "open-foreign-final", "legacy-foreign", "invalid-foreign", "open-unproved-pull", "open-input-foreign-final"} {
		t.Run(mode, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			asset := strings.Repeat("e", 64)
			id := "sha256:" + strings.Repeat("b", 64)
			other := "sha256:" + strings.Repeat("c", 64)
			build := func(path string) error {
				if !strings.HasPrefix(mode, "open") {
					if err := os.WriteFile(path, []byte(id), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "failed-final" || strings.HasPrefix(mode, "open") {
					return fmt.Errorf("fixture build failure")
				}
				return nil
			}
			inspect := func(string) (string, error) { return id, nil }
			var buildErr error
			if mode == "legacy" || mode == "legacy-foreign" {
				buildErr = liverun.CaptureBuildResult(directory, "localhost/sandboxed-agents:none-"+asset, asset, build, inspect)
			} else {
				buildErr = liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, mode == "reuse", build, inspect)
			}
			if (mode == "failed-final" || strings.HasPrefix(mode, "open")) && buildErr == nil {
				t.Fatal("build failure lost")
			}
			if mode == "mixed-reuse" || mode == "repeated-default" {
				if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, mode == "mixed-reuse", build, inspect); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "conflicting" {
				paths, _ := filepath.Glob(filepath.Join(directory, "result-*.json"))
				data, e := os.ReadFile(paths[0])
				if e != nil {
					t.Fatal(e)
				}
				var event map[string]any
				if e = json.Unmarshal(data, &event); e != nil {
					t.Fatal(e)
				}
				event["Result"].(map[string]any)["ID"] = other
				cleanupArtifact(t, directory, "result-conflicting.json", event)
			}
			if mode == "invalid-foreign" {
				cleanupArtifact(t, directory, "result-invalid.json", map[string]string{"forged": "payload"})
			}
			fake.images = map[string]cleanupFakeImage{}
			if mode != "open-empty" && mode != "open-pull" && mode != "open-foreign-final" && mode != "invalid-foreign" && mode != "open-unproved-pull" && mode != "open-input-foreign-final" {
				fake.images[id] = cleanupImageFixture(id)
			}
			if mode == "closed" || mode == "failed-final" || mode == "conflicting" {
				unknown := cleanupImageFixture(other)
				unknown.RepoTags = nil
				fake.images[other] = unknown
			}
			if mode == "open-foreign-final" || mode == "legacy-foreign" || mode == "invalid-foreign" || mode == "open-input-foreign-final" {
				foreign := cleanupImageFixture(other)
				foreign.RepoTags = []string{"other.example/foreign:current"}
				fake.images[other] = foreign
				if mode == "open-input-foreign-final" {
					if err := liverun.RecordRegistryBuildInput(directory, "debian:trixie-slim", other); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "open-pull" || mode == "open-unproved-pull" {
				pulled := cleanupImageFixture(other)
				pulled.RepoTags = []string{"docker.io/library/debian:trixie-slim"}
				pulled.Labels = nil
				fake.images[other] = pulled
				if mode == "open-pull" {
					if err := liverun.RecordRegistryBuildInput(directory, "debian:trixie-slim", other); err != nil {
						t.Fatal(err)
					}
				}
			}
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if e := json.Unmarshal(out.Bytes(), &report); e != nil {
				t.Fatalf("no audit: %v %s", err, &out)
			}
			want := "complete"
			if mode == "open" || mode == "open-foreign-final" || mode == "open-unproved-pull" || mode == "open-input-foreign-final" {
				want = "partial"
			}
			if mode == "reuse" || mode == "legacy" || mode == "conflicting" || mode == "mixed-reuse" || mode == "repeated-default" || mode == "legacy-foreign" || mode == "invalid-foreign" {
				want = "unverified"
			}
			if report.Status != want {
				t.Fatalf("status=%s want=%s err=%v %s", report.Status, want, err, &out)
			}
			if (mode == "open-foreign-final" || mode == "legacy-foreign" || mode == "invalid-foreign" || mode == "open-input-foreign-final") && strings.Contains(out.String(), "not committed by this run's builds") {
				t.Fatalf("arbitrary foreign tag supplied provenance: %s", &out)
			}
			removed := cleanupRemoved(fake)
			if mode == "closed" || mode == "failed-final" {
				if len(removed) != 1 || removed[0] != id || !strings.Contains(out.String(), "not committed by this run's builds") {
					t.Fatalf("wrong closed accounting/removal: %v %s", removed, &out)
				}
			} else if len(removed) != 0 {
				t.Fatalf("unowned/reused result removed: %v %s", removed, &out)
			}
			if mode == "open-pull" && (!strings.Contains(out.String(), other) || !strings.Contains(out.String(), "not committed by this run's builds")) {
				t.Fatalf("FROM input not retained/reported: %s", &out)
			}
		})
	}
}

// TestCleanupPolicyRetainsLayerFreeContext proves the producer-to-report seam names known single-commit policy beside seconds even when another artifact makes coverage unverified.
func TestCleanupPolicyRetainsLayerFreeContext(t *testing.T) {
	for _, kind := range []string{"legacy", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			dir := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, dir, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			id := "sha256:" + strings.Repeat("b", 64)
			asset := strings.Repeat("e", 64)
			if kind == "legacy" {
				if err := liverun.CaptureBuildResult(dir, "localhost/sandboxed-agents:none-"+asset, asset, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				cleanupArtifact(t, dir, "result-invalid.json", map[string]any{"RawOutput": "forged"})
			}
			var out bytes.Buffer
			_ = svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.BuildPolicy != "single-commit-and-unverified" || report.Status != "unverified" {
				t.Fatalf("lost policy context beside seconds: %s", &out)
			}
		})
	}
}
