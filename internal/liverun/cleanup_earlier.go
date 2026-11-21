package liverun

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Absent build results keep a fixed reason; an earlier removal of this run replaces it without ever proving the result.
const (
	cleanupAbsentReason  = "build result absent from complete current inventory"
	cleanupEarlierReason = "removed by an earlier cleanup of this run"
)

// cleanupEarlierLimit caps the earlier cleanups of a run that a rerun reads reports from: only the 16 newest by
// their completion time, so a run directory with many attempts cannot make a rerun read without bound.
// A removal that only an older cleanup lists stays unverified.
const cleanupEarlierLimit = 16

// earlierCleanup names one bound earlier attempt directory and when that cleanup completed.
type earlierCleanup struct {
	name      string
	completed time.Time
}

// earlierRemovals returns the canonical full IDs that bound earlier cleanups of this run removed.
// It reads only real attempt directories inside the run directory through a held root, the current attempt
// excluded, and at most cleanupEarlierLimit reports; a cleanup that fails any check contributes nothing,
// so no earlier report is trusted in part.
func earlierRemovals(run *cleanupRun) map[string]bool {
	removed := map[string]bool{}
	root, err := os.OpenRoot(run.options.RunDir)
	if err != nil {
		return removed
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return removed
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return removed
	}
	candidates := []earlierCleanup{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "cleanup-") || name == filepath.Base(run.directory) {
			continue
		}
		if attempt, completion, ok := openEarlierCleanup(root, name, run); ok {
			attempt.Close()
			candidates = append(candidates, earlierCleanup{name: name, completed: completion.CompletedAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].completed.Equal(candidates[j].completed) {
			return candidates[i].completed.After(candidates[j].completed)
		}
		return candidates[i].name < candidates[j].name
	})
	for _, candidate := range candidates[:min(len(candidates), cleanupEarlierLimit)] {
		for _, id := range earlierCleanupRemovals(root, candidate.name, run) {
			removed[id] = true
		}
	}
	return removed
}

// openEarlierCleanup opens one attempt directory as a held root and admits it only when it is a real directory,
// not a symbolic link, and its completion record binds it to this run's revision, suite end and attempt name.
// The caller closes the returned root.
func openEarlierCleanup(root *os.Root, name string, run *cleanupRun) (*os.Root, cleanupCompletion, bool) {
	var completion cleanupCompletion
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() {
		return nil, completion, false
	}
	attempt, err := root.OpenRoot(name)
	if err != nil {
		return nil, completion, false
	}
	opened, err := attempt.Stat(".")
	if err != nil || !os.SameFile(info, opened) || readEarlierFile(attempt, "completion.json", 64*1024, &completion) != nil {
		attempt.Close()
		return nil, completion, false
	}
	if completion.Revision != run.options.Revision || completion.Attempt != name || completion.Boundary != cleanupTimingBoundary || !completion.SuiteEndedAt.Equal(run.ended.EndedAt) || completion.CompletedAt.Before(run.ended.EndedAt) || completion.CompletedAt.After(time.Now().UTC()) {
		attempt.Close()
		return nil, completion, false
	}
	return attempt, completion, true
}

// earlierCleanupRemovals returns the removed IDs of one earlier cleanup only when its completion record and its
// private report, both read through the same held attempt directory, pass every check: the report decodes strictly,
// matches this run's exit evidence and before measurement, and names every removed image by its full canonical ID.
// An ID that the same report also lists as unverified is left out.
func earlierCleanupRemovals(root *os.Root, name string, run *cleanupRun) []string {
	attempt, _, ok := openEarlierCleanup(root, name, run)
	if !ok {
		return nil
	}
	defer attempt.Close()
	var report CleanupReport
	if readEarlierFile(attempt, "cleanup-report.json", cleanupSummaryLimit, &report) != nil {
		return nil
	}
	if report.SuiteExitCode != run.ended.Code || report.Interrupted != run.ended.Interrupted || !reflect.DeepEqual(report.Before, run.report.Before) {
		return nil
	}
	unverified := map[string]bool{}
	for _, item := range report.Unverified {
		unverified[item.ID] = true
	}
	ids := []string{}
	for _, item := range report.Removed {
		if !fullImageID(item.ID) || canonicalImageID(item.ID) != item.ID {
			return nil
		}
		if !unverified[item.ID] {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// readEarlierFile strictly decodes one regular file of a held attempt directory. It refuses a symbolic link and a
// file replaced between its check and its opening, so the bytes read are those of the checked file.
func readEarlierFile(attempt *os.Root, name string, limit int, value any) error {
	info, err := attempt.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("earlier cleanup artifact is not a regular file")
	}
	file, err := attempt.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("earlier cleanup artifact changed while opened")
	}
	data, err := cleanupReadOpen(file, limit)
	if err != nil {
		return err
	}
	return strictDecode(data, value)
}

// markEarlierRemovals changes only the absent reason of a full-ID result that an earlier cleanup of this run removed.
// The new reason still leaves the result unproven, so it grants no build proof and no removal.
func markEarlierRemovals(record []buildProof, earlier map[string]bool) {
	for i := range record {
		id := canonicalImageID(record[i].Token)
		if record[i].Reason == cleanupAbsentReason && record[i].FullID == "" && fullImageID(id) && earlier[id] {
			record[i].Reason = cleanupEarlierReason
		}
	}
}
