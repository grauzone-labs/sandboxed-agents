package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// cleanupRerunReport runs Cleanup once more and decodes the sanitized report it printed.
func cleanupRerunReport(t *testing.T, svc *liverun.Service, options liverun.CleanupOptions) (liverun.CleanupReport, error) {
	t.Helper()
	var out bytes.Buffer
	err := svc.Cleanup(context.Background(), options, &out)
	var report liverun.CleanupReport
	if parseErr := json.Unmarshal(out.Bytes(), &report); parseErr != nil {
		t.Fatalf("report missing: %v %v %s", parseErr, err, &out)
	}
	return report, err
}

// TestCleanupRerunCountsEarlierRemovalOfThisRun proves that a second Cleanup of the same run reports a build result
// the first cleanup removed as removed earlier, without an unverified item, a non-complete status, a second removal
// or counted bytes. It exercises the Cleanup command seam against the fake engine process boundary.
func TestCleanupRerunCountsEarlierRemovalOfThisRun(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	if first, err := cleanupRerunReport(t, svc, options); err != nil || len(first.Removed) != 1 {
		t.Fatalf("first cleanup did not remove the proven result: %v %+v", err, first)
	}
	fake.calls = nil
	report, err := cleanupRerunReport(t, svc, options)
	if err != nil || report.Status != "complete" || len(report.Unverified) != 0 || len(report.Removed) != 0 || len(cleanupRemoved(fake)) != 0 {
		t.Fatalf("rerun overstated an earlier removal: %v %+v", err, report)
	}
	want := liverun.CleanupItem{ID: id, Reason: "removed by an earlier cleanup of this run"}
	if len(report.RemovedEarlier) != 1 || report.RemovedEarlier[0].ID != want.ID || report.RemovedEarlier[0].Reason != want.Reason || report.RemovedEarlier[0].SizeBytes != 0 || report.RemovedEarlier[0].SizeVerified {
		t.Fatalf("earlier removal not reported distinctly: %+v", report.RemovedEarlier)
	}
}

// cleanupAttemptDir returns the single attempt directory that the earlier Cleanup left in the run directory.
func cleanupAttemptDir(t *testing.T, run string) string {
	t.Helper()
	entries, err := os.ReadDir(run)
	if err != nil {
		t.Fatal(err)
	}
	found := []string{}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "cleanup-") {
			found = append(found, filepath.Join(run, entry.Name()))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one attempt directory, got %v", found)
	}
	return found[0]
}

// cleanupEditEarlierReport rewrites one field of the earlier attempt's private report as literal JSON.
func cleanupEditEarlierReport(t *testing.T, attempt string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(attempt, "cleanup-report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	edit(report)
	cleanupArtifact(t, attempt, "cleanup-report.json", report)
}

// TestCleanupRerunDistrustsUnboundEarlierReport proves that an absent build result stays unverified, exactly as
// before, unless a valid earlier cleanup report of this same run lists its full ID as removed, and that such a
// report never makes a present image eligible. It exercises the Cleanup command seam against the fake engine
// process boundary, editing only the private attempt artifacts the first cleanup left.
func TestCleanupRerunDistrustsUnboundEarlierReport(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("c", 64)
	for _, name := range []string{"notlisted", "malformed", "unknownfield", "partlymalformed", "tagonly", "listedunverified", "otherexit", "nocompletion", "foreignrun", "symlink", "present"} {
		t.Run(name, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
			cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
			if name == "foreignrun" {
				// Another run, identical except for its suite end, removes the same ID; its attempt is copied in.
				foreignSvc, foreignFake, foreignOptions := cleanupHarness(t)
				cleanupArtifact(t, foreignOptions.RunDir, "exit.json", map[string]any{"Code": 0, "EndedAt": time.Now().UTC().Add(-30 * time.Second), "Interrupted": false})
				foreignFake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
				cleanupBuildLog(t, foreignFake, foreignOptions.RunDir, "STEP 2/3: LABEL example=value", id)
				if first, err := cleanupRerunReport(t, foreignSvc, foreignOptions); err != nil || len(first.Removed) != 1 {
					t.Fatalf("foreign cleanup did not remove: %v %+v", err, first)
				}
				source := cleanupAttemptDir(t, foreignOptions.RunDir)
				if err := os.CopyFS(filepath.Join(options.RunDir, filepath.Base(source)), os.DirFS(source)); err != nil {
					t.Fatal(err)
				}
				fake.images = map[string]cleanupFakeImage{}
			} else if first, err := cleanupRerunReport(t, svc, options); err != nil || len(first.Removed) != 1 {
				t.Fatalf("first cleanup did not remove the proven result: %v %+v", err, first)
			}
			attempt := cleanupAttemptDir(t, options.RunDir)
			switch name {
			case "notlisted":
				// The earlier report is valid; only the ID it never removed stays unverified.
				fake.produced = append(fake.produced, liverun.BuildResult{ID: other, Step: "primary result", Source: "trusted fixture build producer"})
			case "malformed":
				if err := os.WriteFile(filepath.Join(attempt, "cleanup-report.json"), []byte(`{"removed":[{"id":"`+id+`"`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknownfield":
				cleanupEditEarlierReport(t, attempt, func(report map[string]any) { report["run"] = "another" })
			case "partlymalformed":
				cleanupEditEarlierReport(t, attempt, func(report map[string]any) {
					report["removed"] = append(report["removed"].([]any), map[string]any{"id": id[:19], "size_bytes": 1, "size_verified": true})
				})
			case "tagonly":
				cleanupEditEarlierReport(t, attempt, func(report map[string]any) {
					report["removed"] = []any{map[string]any{"tags": []string{"localhost/sandboxed-agents:none-" + strings.Repeat("e", 64)}, "size_bytes": 123, "size_verified": true}}
				})
			case "listedunverified":
				cleanupEditEarlierReport(t, attempt, func(report map[string]any) {
					report["unverified"] = []any{map[string]any{"id": id, "size_bytes": 0, "size_verified": false, "reason": "removal output disagrees with final inventory"}}
				})
			case "otherexit":
				cleanupEditEarlierReport(t, attempt, func(report map[string]any) { report["suite_exit_code"] = 1 })
			case "nocompletion":
				if err := os.Remove(filepath.Join(attempt, "completion.json")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				// The same attempt, moved outside the run directory and linked back under its own name.
				outside := filepath.Join(t.TempDir(), filepath.Base(attempt))
				if err := os.Rename(attempt, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, attempt); err != nil {
					t.Skipf("symbolic links unavailable: %v", err)
				}
			case "present":
				// The removed ID is present again with no build proof of this attempt.
				fake.images[id] = cleanupImageFixture(id)
				fake.produced = nil
			}
			fake.calls = nil
			report, err := cleanupRerunReport(t, svc, options)
			if len(cleanupRemoved(fake)) != 0 {
				t.Fatalf("earlier report granted a removal: %v %+v", cleanupRemoved(fake), report)
			}
			if name == "present" {
				if err == nil || report.Status != "partial" || len(report.RemovedEarlier) != 0 || len(report.Skipped) != 1 || report.Skipped[0].ID != id || report.Skipped[0].Reason != "missing exact build proof" {
					t.Fatalf("present image changed by an earlier report: %v %+v", err, report)
				}
				return
			}
			absent := id
			if name == "notlisted" {
				absent = other
				if len(report.RemovedEarlier) != 1 || report.RemovedEarlier[0].ID != id {
					t.Fatalf("valid earlier removal lost: %+v", report.RemovedEarlier)
				}
			} else if len(report.RemovedEarlier) != 0 {
				t.Fatalf("unbound earlier report trusted: %+v", report.RemovedEarlier)
			}
			if err == nil || report.Status != "unverified" || len(report.Unverified) != 1 || report.Unverified[0].ID != absent || report.Unverified[0].Reason != "build result absent from complete current inventory" {
				t.Fatalf("absent result not unverified as before: %v %+v", err, report)
			}
		})
	}
}
