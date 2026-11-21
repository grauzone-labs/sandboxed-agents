package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestBlockedPrimaryReceiptReportsActualVeto proves journal-backed vetoes remain noneligible and report the same truthful skipped/plan reason at the producer-to-Cleanup seam.
func TestBlockedPrimaryReceiptReportsActualVeto(t *testing.T) {
	for _, mode := range []string{"reuse", "repeated", "input", "retained", "before"} {
		t.Run(mode, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			id := "sha256:" + strings.Repeat("b", 64)
			independent := "sha256:" + strings.Repeat("d", 64)
			asset := strings.Repeat("e", 64)
			tag := "localhost/sandboxed-agents:none-" + asset
			for _, imageID := range []string{id, independent} {
				if err := liverun.CaptureSingleCommitBuild(directory, tag, asset, imageID == id && (mode == "reuse" || mode == "retained" || mode == "before"), func(path string) error { return os.WriteFile(path, []byte(imageID), 0600) }, func(string) (string, error) { return imageID, nil }); err != nil {
					t.Fatal(err)
				}
			}
			want := "primary result reuse not excluded; retained"
			if mode == "repeated" {
				if err := liverun.CaptureSingleCommitBuild(directory, tag, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
					t.Fatal(err)
				}
				want = "primary result shared by distinct build attempts; retained"
			}
			if mode == "input" {
				if err := liverun.RecordRegistryBuildInput(directory, "debian:trixie-slim", id); err != nil {
					t.Fatal(err)
				}
				want = "primary result collides with recorded registry input; retained"
			}
			if mode == "retained" {
				options.Retain = []string{id}
				want = "retained by user"
			}
			if mode == "before" {
				path := filepath.Join(options.RunDir, "before.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var before liverun.Snapshot
				if err = json.Unmarshal(data, &before); err != nil {
					t.Fatal(err)
				}
				before.Images = append(before.Images, liverun.Image{ID: id, Created: before.TakenAt})
				cleanupArtifact(t, options.RunDir, "before.json", before)
				want = "present before invocation"
			}
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), independent: cleanupImageFixture(independent)}
			var out bytes.Buffer
			_ = svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("no audit: %v %s", err, &out)
			}
			removed := cleanupRemoved(fake)
			if report.Status != "unverified" || len(removed) != 1 || removed[0] != independent || len(report.Skipped) != 1 || report.Skipped[0].ID != id || report.Skipped[0].Reason != want {
				t.Fatalf("wrong veto/report: want=%q removed=%v %s", want, removed, &out)
			}
			paths, _ := filepath.Glob(filepath.Join(options.RunDir, "cleanup-*", "removal-plan.json"))
			if len(paths) != 1 {
				t.Fatalf("missing plan: %v", paths)
			}
			data, err := os.ReadFile(paths[0])
			var plan []liverun.CleanupItem
			if err != nil || json.Unmarshal(data, &plan) != nil {
				t.Fatalf("unreadable plan: %v", err)
			}
			found := false
			for _, item := range plan {
				if item.ID == id {
					found = true
					if item.Reason != want {
						t.Fatalf("plan reason differs: %+v", item)
					}
				}
			}
			if !found {
				t.Fatal("plan omitted blocked image")
			}
		})
	}
}

// TestClosedJournalExplainsOnlyPrimaryResultsAndRegistryInputs proves absent-before coverage is conservative across tag forms and preserves before/retention reasons at the real journal-to-Cleanup seam.
func TestClosedJournalExplainsOnlyPrimaryResultsAndRegistryInputs(t *testing.T) {
	for _, mode := range []string{"canonical", "foreign", "tagless", "known-input", "before-foreign", "retained-foreign", "retained-input", "read-failure", "unverified-input"} {
		t.Run(mode, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			id := "sha256:" + strings.Repeat("b", 64)
			other := "sha256:" + strings.Repeat("c", 64)
			asset := strings.Repeat("e", 64)
			if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Fatal(err)
			}
			image := cleanupImageFixture(other)
			wantStatus, wantReason := "partial", "foreign or mixed image tags"
			image.RepoTags = []string{"private.example/foreign:current"}
			switch mode {
			case "canonical":
				image.RepoTags = []string{"localhost/sandboxed-agents:dotnet-" + asset}
				wantReason = "missing exact build proof"
			case "tagless":
				image.RepoTags, image.Labels = nil, nil
				wantReason = "missing exact build proof"
			case "known-input", "retained-input", "unverified-input":
				image.RepoTags = []string{"docker.io/library/debian:trixie-slim"}
				image.Labels = nil
				if err := liverun.RecordRegistryBuildInput(directory, "debian:trixie-slim", other); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantReason = "complete", "not committed by this run's builds"
			case "before-foreign":
				data, err := os.ReadFile(filepath.Join(options.RunDir, "before.json"))
				if err != nil {
					t.Fatal(err)
				}
				var before liverun.Snapshot
				if err = json.Unmarshal(data, &before); err != nil {
					t.Fatal(err)
				}
				before.Images = append(before.Images, liverun.Image{ID: other, Created: before.TakenAt, RepoTags: image.RepoTags})
				cleanupArtifact(t, options.RunDir, "before.json", before)
				wantStatus, wantReason = "complete", "present before invocation"
			}
			if mode == "read-failure" || mode == "unverified-input" {
				fake.fail = other
				wantStatus, wantReason = "unverified", "image inspection failed; unverified"
			}
			if mode == "retained-foreign" || mode == "retained-input" {
				options.Retain = []string{other}
				wantReason = "retained by user"
			}
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), other: image}
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if e := json.Unmarshal(out.Bytes(), &report); e != nil {
				t.Fatalf("no audit: %v %s", err, &out)
			}
			removed := cleanupRemoved(fake)
			if report.Status != wantStatus || (err == nil) != (wantStatus == "complete") || len(removed) != 1 || removed[0] != id || len(report.Skipped) != 1 || report.Skipped[0].ID != other || report.Skipped[0].Reason != wantReason {
				t.Fatalf("mode=%s want=%s/%s removed=%v err=%v %s", mode, wantStatus, wantReason, removed, err, &out)
			}
			if strings.Contains(out.String(), "private.example") || strings.Contains(out.String(), "docker.io/library/debian") {
				t.Fatal("foreign host tag leaked")
			}
			paths, _ := filepath.Glob(filepath.Join(options.RunDir, "cleanup-*", "removal-plan.json"))
			if len(paths) != 1 {
				t.Fatalf("missing plan: %v", paths)
			}
			data, e := os.ReadFile(paths[0])
			var plan []liverun.CleanupItem
			if e != nil || json.Unmarshal(data, &plan) != nil {
				t.Fatalf("bad plan: %v", e)
			}
			found := false
			for _, item := range plan {
				if item.ID == other {
					found = true
					if item.Reason != wantReason {
						t.Fatalf("plan reason differs: %+v", item)
					}
				}
			}
			if !found {
				t.Fatal("plan omitted retained image")
			}
		})
	}
}

// TestClosedChannelCannotExcludeUnreceiptedProjectFinal proves canonical current-asset finals without a receipt retain unexplained coverage at the real journal/inventory-to-Cleanup seam, without deriving ownership from tags or labels.
func TestClosedChannelCannotExcludeUnreceiptedProjectFinal(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "mixed"}[mixed], func(t *testing.T) {
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
			if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Fatal(err)
			}
			unreceipted := cleanupImageFixture(unknown)
			want := "missing exact build proof"
			if mixed {
				unreceipted.RepoTags = append(unreceipted.RepoTags, "private.example/foreign:tag")
				want = "foreign or mixed image tags"
			}
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), unknown: unreceipted}
			var out bytes.Buffer
			_ = svc.Cleanup(context.Background(), options, &out)
			var report liverun.CleanupReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("no audit: %v %s", err, &out)
			}
			removed := cleanupRemoved(fake)
			if report.Status != "partial" || len(removed) != 1 || removed[0] != id || len(report.Skipped) != 1 || report.Skipped[0].ID != unknown || report.Skipped[0].Reason != want {
				t.Fatalf("unreceipted current-asset final falsely excluded: removed=%v %s", removed, &out)
			}
			if strings.Contains(out.String(), "private.example") {
				t.Fatal("foreign tag leaked")
			}
		})
	}
}

// TestMisnamedAttemptQuarantinesBothIdentities proves conflicting filename/content attempt identities retain both associated images while production Cleanup still removes an independent final at the journal seam.
func TestMisnamedAttemptQuarantinesBothIdentities(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	svc.BuildEvidence = nil
	directory := filepath.Join(options.RunDir, "build-evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
	first := "sha256:" + strings.Repeat("b", 64)
	second := "sha256:" + strings.Repeat("c", 64)
	independent := "sha256:" + strings.Repeat("d", 64)
	asset := strings.Repeat("e", 64)
	for _, id := range []string{first, second, independent} {
		if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
			t.Fatal(err)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(directory, "result-*.json"))
	events := map[string]map[string]any{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err = json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		events[event["Result"].(map[string]any)["ID"].(string)] = event
	}
	if len(events) != 3 {
		t.Fatalf("fixture missing receipts: %v", paths)
	}
	fileID := events[second]["Result"].(map[string]any)["AttemptID"].(string)
	content := events[first]["Result"].(map[string]any)
	content["ID"], content["Outcome"], content["EndedAt"] = "", "", "0001-01-01T00:00:00Z"
	cleanupArtifact(t, directory, "attempt-"+fileID+".json", events[first])
	fake.images = map[string]cleanupFakeImage{first: cleanupImageFixture(first), second: cleanupImageFixture(second), independent: cleanupImageFixture(independent)}
	var out bytes.Buffer
	_ = svc.Cleanup(context.Background(), options, &out)
	var report liverun.CleanupReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("no audit: %v %s", err, &out)
	}
	removed := cleanupRemoved(fake)
	if report.Status != "unverified" || len(removed) != 1 || removed[0] != independent || len(report.Skipped) != 2 {
		t.Fatalf("ambiguous filename identity admitted or independent proof lost: removed=%v %s", removed, &out)
	}
	for _, item := range report.Skipped {
		if item.Reason != "missing exact build proof" {
			t.Fatalf("ambiguous identity falsely excluded: %+v", item)
		}
	}
}
