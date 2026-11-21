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

// cleanupEditEarlier rewrites one private JSON file of the earlier attempt as literal JSON.
func cleanupEditEarlier(t *testing.T, attempt, name string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(attempt, name))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	edit(value)
	cleanupArtifact(t, attempt, name, value)
}

// cleanupRelink moves path to stash, a relative path beside it that stays inside the run directory, and links it
// back under its own name, so only the explicit link checks refuse it; it skips the test when the platform refuses
// symbolic links.
func cleanupRelink(t *testing.T, path, stash string) {
	t.Helper()
	moved := filepath.Join(filepath.Dir(path), stash)
	if err := os.MkdirAll(filepath.Dir(moved), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stash, path); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
}

// cleanupSuiteEnd returns the suite end that the earlier attempt's completion record names.
func cleanupSuiteEnd(t *testing.T, attempt string) time.Time {
	t.Helper()
	var completion struct{ SuiteEndedAt time.Time }
	data, err := os.ReadFile(filepath.Join(attempt, "completion.json"))
	if err != nil || json.Unmarshal(data, &completion) != nil {
		t.Fatalf("completion unreadable: %v", err)
	}
	return completion.SuiteEndedAt
}

// TestCleanupRerunDistrustsUnboundEarlierReport proves that an absent build result stays unverified, exactly as
// before, unless an earlier cleanup of this same run, whose completion record and private report pass every check,
// lists its full ID as removed. Each subtest breaks one check. It exercises the Cleanup command seam against the
// fake engine process boundary, editing only the private attempt artifacts the first cleanup left.
func TestCleanupRerunDistrustsUnboundEarlierReport(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("c", 64)
	names := []string{"notlisted", "malformed", "unknownfield", "trailingdata", "partlymalformed", "tagonly", "barehex", "listedunverified", "otherexit", "otherinterrupted", "otherbefore",
		"nocompletion", "malformedcompletion", "completionunknownfield", "otherrevision", "otherboundary", "futurecompletion", "earlycompletion", "renamedattempt",
		"foreignrun", "symlink", "completionsymlink", "reportsymlink"}
	for _, name := range names {
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
			report := func(edit func(map[string]any)) { cleanupEditEarlier(t, attempt, "cleanup-report.json", edit) }
			completion := func(edit func(map[string]any)) { cleanupEditEarlier(t, attempt, "completion.json", edit) }
			switch name {
			case "notlisted":
				// The earlier report is valid; only the ID it never removed stays unverified.
				fake.produced = append(fake.produced, liverun.BuildResult{ID: other, Step: "primary result", Source: "trusted fixture build producer"})
			case "malformed":
				if err := os.WriteFile(filepath.Join(attempt, "cleanup-report.json"), []byte(`{"removed":[{"id":"`+id+`"`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknownfield":
				report(func(value map[string]any) { value["run"] = "another" })
			case "trailingdata":
				file, err := os.OpenFile(filepath.Join(attempt, "cleanup-report.json"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.WriteString("\n{}"); err != nil {
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
			case "partlymalformed":
				report(func(value map[string]any) {
					value["removed"] = append(value["removed"].([]any), map[string]any{"id": id[:19], "size_bytes": 1, "size_verified": true})
				})
			case "tagonly":
				report(func(value map[string]any) {
					value["removed"] = []any{map[string]any{"tags": []string{"localhost/sandboxed-agents:none-" + strings.Repeat("e", 64)}, "size_bytes": 123, "size_verified": true}}
				})
			case "barehex":
				// A bare 64-hex ID beside the valid full ID rejects the whole report.
				report(func(value map[string]any) {
					value["removed"] = append(value["removed"].([]any), map[string]any{"id": strings.TrimPrefix(other, "sha256:"), "size_bytes": 1, "size_verified": true})
				})
			case "listedunverified":
				report(func(value map[string]any) {
					value["unverified"] = []any{map[string]any{"id": id, "size_bytes": 0, "size_verified": false, "reason": "removal output disagrees with final inventory"}}
				})
			case "otherexit":
				report(func(value map[string]any) { value["suite_exit_code"] = 1 })
			case "otherinterrupted":
				report(func(value map[string]any) { value["interrupted"] = true })
			case "otherbefore":
				report(func(value map[string]any) {
					value["before"].(map[string]any)["free_space"].([]any)[0].(map[string]any)["free_bytes"] = 901
				})
			case "nocompletion":
				if err := os.Remove(filepath.Join(attempt, "completion.json")); err != nil {
					t.Fatal(err)
				}
			case "malformedcompletion":
				if err := os.WriteFile(filepath.Join(attempt, "completion.json"), []byte(`{"Revision":`), 0600); err != nil {
					t.Fatal(err)
				}
			case "completionunknownfield":
				completion(func(value map[string]any) { value["Run"] = "another" })
			case "otherrevision":
				completion(func(value map[string]any) { value["Revision"] = strings.Repeat("d", 40) })
			case "otherboundary":
				completion(func(value map[string]any) { value["Boundary"] = "after collector" })
			case "futurecompletion":
				completion(func(value map[string]any) { value["CompletedAt"] = time.Now().UTC().Add(time.Hour) })
			case "earlycompletion":
				end := cleanupSuiteEnd(t, attempt)
				completion(func(value map[string]any) { value["CompletedAt"] = end.Add(-time.Second) })
			case "renamedattempt":
				// The whole attempt, completion record included, now sits under another attempt name.
				if err := os.Rename(attempt, filepath.Join(options.RunDir, "cleanup-renamed")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				// The same attempt, moved to another directory of the run and linked back under its own name.
				cleanupRelink(t, attempt, filepath.Join("stash", filepath.Base(attempt)))
			case "completionsymlink":
				cleanupRelink(t, filepath.Join(attempt, "completion.json"), "stash-completion.json")
			case "reportsymlink":
				cleanupRelink(t, filepath.Join(attempt, "cleanup-report.json"), "stash-report.json")
			}
			fake.calls = nil
			got, err := cleanupRerunReport(t, svc, options)
			if len(cleanupRemoved(fake)) != 0 {
				t.Fatalf("earlier report granted a removal: %v %+v", cleanupRemoved(fake), got)
			}
			absent := id
			if name == "notlisted" {
				absent = other
				if len(got.RemovedEarlier) != 1 || got.RemovedEarlier[0].ID != id {
					t.Fatalf("valid earlier removal lost: %+v", got.RemovedEarlier)
				}
			} else if len(got.RemovedEarlier) != 0 {
				t.Fatalf("unbound earlier report trusted: %+v", got.RemovedEarlier)
			}
			if err == nil || got.Status != "unverified" || len(got.Unverified) != 1 || got.Unverified[0].ID != absent || got.Unverified[0].Reason != "build result absent from complete current inventory" {
				t.Fatalf("absent result not unverified as before: %v %+v", err, got)
			}
		})
	}
}

// TestCleanupRerunNeverRelabelsPresentResult proves that an earlier report listing a present image as removed
// changes nothing about that image: a proven one is removed by the ordinary rules, an uninspectable or vetoed one
// keeps its own reason, an unproven one stays skipped, and none is listed under removed_earlier. It exercises the
// Cleanup command seam against the fake engine process boundary and, for the veto, the real build-evidence channel.
func TestCleanupRerunNeverRelabelsPresentResult(t *testing.T) {
	id := "sha256:" + strings.Repeat("b", 64)
	for _, name := range []string{"proven", "unproven", "uninspectable", "vetoed"} {
		t.Run(name, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			fake.images = map[string]cleanupFakeImage{}
			if first, err := cleanupRerunReport(t, svc, options); err != nil || first.Status != "complete" {
				t.Fatalf("first cleanup: %v %+v", err, first)
			}
			cleanupEditEarlier(t, cleanupAttemptDir(t, options.RunDir), "cleanup-report.json", func(value map[string]any) {
				value["removed"] = []any{map[string]any{"id": id, "size_bytes": 123, "size_verified": true}}
			})
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
			fake.produced = []liverun.BuildResult{{ID: id, Step: "primary result", Source: "trusted fixture build producer"}}
			want := ""
			switch name {
			case "unproven":
				fake.produced = nil
			case "uninspectable":
				fake.fail = "image inspect"
				want = "image inspection failed; build result unverified"
			case "vetoed":
				svc.BuildEvidence = nil
				directory := filepath.Join(options.RunDir, "build-evidence")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
				asset := strings.Repeat("e", 64)
				if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, true, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
					t.Fatal(err)
				}
				want = "primary result reuse not excluded; retained"
			}
			fake.calls = nil
			report, err := cleanupRerunReport(t, svc, options)
			if len(report.RemovedEarlier) != 0 {
				t.Fatalf("present image listed as removed earlier: %+v", report)
			}
			removed := cleanupRemoved(fake)
			switch name {
			case "proven":
				if err != nil || len(removed) != 1 || removed[0] != id || len(report.Removed) != 1 || report.Removed[0].SizeBytes != 123 {
					t.Fatalf("proven present image not removed by the ordinary rules: %v %v %+v", err, removed, report)
				}
				return
			case "unproven":
				if err == nil || len(removed) != 0 || report.Status != "partial" || len(report.Skipped) != 1 || report.Skipped[0].ID != id || report.Skipped[0].Reason != "missing exact build proof" {
					t.Fatalf("unproven present image changed by an earlier report: %v %+v", err, report)
				}
				return
			}
			found := false
			for _, item := range report.Unverified {
				found = found || item.ID == id && item.Reason == want
			}
			if err == nil || len(removed) != 0 || !found {
				t.Fatalf("present image lost its reason %q: %v %v %+v", want, err, removed, report)
			}
		})
	}
}

// TestCleanupRerunRechecksEarlierRemovalInFinalInventory proves that an image listed under removed_earlier which
// the final inventory shows again is reported as unverified and is not removed. It exercises the Cleanup command
// seam with the fake engine creating the image between the first and the final inventory.
func TestCleanupRerunRechecksEarlierRemovalInFinalInventory(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	if first, err := cleanupRerunReport(t, svc, options); err != nil || len(first.Removed) != 1 {
		t.Fatalf("first cleanup did not remove the proven result: %v %+v", err, first)
	}
	fake.calls = nil
	fake.imagesCalls = 0
	fake.lateImage = id
	report, err := cleanupRerunReport(t, svc, options)
	if len(cleanupRemoved(fake)) != 0 || len(report.RemovedEarlier) != 1 || report.RemovedEarlier[0].ID != id {
		t.Fatalf("rerun changed the earlier removal: %v %+v", cleanupRemoved(fake), report)
	}
	want := liverun.CleanupItem{ID: id, Reason: "removed earlier but present in final inventory", Query: "podman images --all --no-trunc"}
	if err == nil || report.Status != "unverified" || len(report.Unverified) != 1 || report.Unverified[0].ID != want.ID || report.Unverified[0].Reason != want.Reason || report.Unverified[0].Query != want.Query {
		t.Fatalf("reappeared image not unverified: %v %+v", err, report)
	}
}

// TestCleanupRerunStaysCompleteAcrossManyReruns proves that reruns which remove nothing never push the cleanup that
// removed an image out of the earlier cleanups read, so twenty sequential cleanups of one run all end complete with
// the image under removed_earlier from the second on. It exercises the Cleanup command seam against the fake engine.
func TestCleanupRerunStaysCompleteAcrossManyReruns(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
	cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
	for run := 1; run <= 20; run++ {
		report, err := cleanupRerunReport(t, svc, options)
		if run == 1 {
			if err != nil || len(report.Removed) != 1 {
				t.Fatalf("first cleanup did not remove the proven result: %v %+v", err, report)
			}
			continue
		}
		if err != nil || report.Status != "complete" || len(report.Removed) != 0 || len(report.RemovedEarlier) != 1 || report.RemovedEarlier[0].ID != id {
			t.Fatalf("cleanup %d lost the earlier removal: %v %+v", run, err, report)
		}
	}
}

// TestCleanupRerunReadsOnlyNewestContributingCleanups proves that a rerun reads the removals of at most the 16 newest
// bound earlier cleanups that removed something, so with 16 newer such cleanups a removal that only the oldest lists
// stays unverified. It exercises the Cleanup command seam with copies of a real attempt, each renamed, given a later
// completion time and a removal of another image.
func TestCleanupRerunReadsOnlyNewestContributingCleanups(t *testing.T) {
	for _, newer := range []int{15, 16} {
		t.Run(fmt.Sprint(newer), func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			id := "sha256:" + strings.Repeat("b", 64)
			other := "sha256:" + strings.Repeat("c", 64)
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id)}
			cleanupBuildLog(t, fake, options.RunDir, "STEP 2/3: LABEL example=value", id)
			if first, err := cleanupRerunReport(t, svc, options); err != nil || len(first.Removed) != 1 {
				t.Fatalf("first cleanup did not remove the proven result: %v %+v", err, first)
			}
			source := cleanupAttemptDir(t, options.RunDir)
			for i := range newer {
				name := fmt.Sprintf("cleanup-newer%02d", i)
				copied := filepath.Join(options.RunDir, name)
				if err := os.CopyFS(copied, os.DirFS(source)); err != nil {
					t.Fatal(err)
				}
				cleanupEditEarlier(t, copied, "cleanup-report.json", func(value map[string]any) {
					value["removed"] = []any{map[string]any{"id": other, "size_bytes": 1, "size_verified": true}}
				})
				cleanupEditEarlier(t, copied, "completion.json", func(value map[string]any) {
					completed, err := time.Parse(time.RFC3339Nano, value["CompletedAt"].(string))
					if err != nil {
						t.Fatal(err)
					}
					value["Attempt"] = name
					value["CompletedAt"] = completed.Add(time.Duration(i+1) * time.Millisecond)
				})
			}
			report, err := cleanupRerunReport(t, svc, options)
			if newer < 16 {
				if err != nil || len(report.RemovedEarlier) != 1 || report.RemovedEarlier[0].ID != id {
					t.Fatalf("earlier removal within the cap lost: %v %+v", err, report)
				}
				return
			}
			if err == nil || len(report.RemovedEarlier) != 0 || len(report.Unverified) != 1 || report.Unverified[0].ID != id {
				t.Fatalf("earlier cleanup beyond the cap read: %v %+v", err, report)
			}
		})
	}
}
