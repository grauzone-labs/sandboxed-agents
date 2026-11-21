package liverun

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// Absent build results keep a fixed reason; an earlier removal of this run replaces it without ever proving the result.
const (
	cleanupAbsentReason  = "build result absent from complete current inventory"
	cleanupEarlierReason = "removed by an earlier cleanup of this run"
)

// earlierRemovals returns the canonical full IDs that completed earlier cleanup attempts of this run removed.
// Only real attempt directories inside the run directory are read, the current attempt excluded; an attempt
// that fails any check contributes nothing, so no earlier report is trusted in part.
func earlierRemovals(run *cleanupRun) map[string]bool {
	removed := map[string]bool{}
	entries, err := os.ReadDir(run.options.RunDir)
	if err != nil {
		return removed
	}
	for _, entry := range entries {
		directory := filepath.Join(run.options.RunDir, entry.Name())
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "cleanup-") || directory == run.directory {
			continue
		}
		for _, id := range earlierAttemptRemovals(run, directory) {
			removed[id] = true
		}
	}
	return removed
}

// earlierAttemptRemovals returns the removed IDs of one earlier attempt only when its completion record binds it
// to this run's revision, suite end and attempt name, and its private report decodes strictly, matches this run's
// exit evidence and before measurement, and names every removed image by its full canonical ID.
// An ID that the same report also lists as unverified is left out.
func earlierAttemptRemovals(run *cleanupRun, directory string) []string {
	var completion cleanupCompletion
	if !cleanupRegular(filepath.Join(directory, "completion.json")) || strictBuildJSON(filepath.Join(directory, "completion.json"), &completion) != nil {
		return nil
	}
	if completion.Revision != run.options.Revision || completion.Attempt != filepath.Base(directory) || completion.Boundary != cleanupTimingBoundary || !completion.SuiteEndedAt.Equal(run.ended.EndedAt) || completion.CompletedAt.Before(run.ended.EndedAt) || completion.CompletedAt.After(time.Now().UTC()) {
		return nil
	}
	var report CleanupReport
	path := filepath.Join(directory, "cleanup-report.json")
	if !cleanupRegular(path) || strictJSON(path, cleanupSummaryLimit, &report) != nil {
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

// cleanupRegular reports whether path itself is a regular file, refusing a symbolic link before it is followed.
func cleanupRegular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
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
