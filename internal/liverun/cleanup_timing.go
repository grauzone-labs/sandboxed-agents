package liverun

import (
	"path/filepath"
	"time"
)

// cleanupTimingBoundary names the measured clock after collection, main report writes and cleanup teardown.
const cleanupTimingBoundary = "after collector, cleanup report publication, and cleanup return-path teardown"

// cleanupCompletion retains a private clock for one completed cleanup attempt, including its observed suite-end boundary.
type cleanupCompletion struct {
	Revision              string
	Attempt               string
	SuiteEndedAt          time.Time
	CompletedAt           time.Time
	PostSuiteTotalSeconds float64
	Boundary              string
}

// retainCleanupCompletion records timing after the main report has reached its writer; timing loss cannot change cleanup's result.
func retainCleanupCompletion(run *cleanupRun) {
	if !run.published {
		return
	}
	completed := time.Now().UTC()
	if completed.Before(run.ended.EndedAt) {
		return
	}
	record := cleanupCompletion{Revision: run.options.Revision, Attempt: filepath.Base(run.directory), SuiteEndedAt: run.ended.EndedAt, CompletedAt: completed, PostSuiteTotalSeconds: completed.Sub(run.ended.EndedAt).Seconds(), Boundary: cleanupTimingBoundary}
	if writeJSON(filepath.Join(run.directory, "completion.json"), record) != nil {
		return
	}
	_ = writeJSON(filepath.Join(run.options.RunDir, "cleanup-completion.json"), record)
}

// readCleanupTotal admits only a clock bound to this prepared run and authoritative exit evidence.
func readCleanupTotal(directory string, o Options, ended exitEvidence) (float64, bool) {
	var record cleanupCompletion
	if strictBuildJSON(filepath.Join(directory, "cleanup-completion.json"), &record) != nil || record.Revision != o.Revision || record.Boundary != cleanupTimingBoundary || !record.SuiteEndedAt.Equal(ended.EndedAt) || record.CompletedAt.Before(ended.EndedAt) || record.CompletedAt.After(time.Now().UTC()) {
		return 0, false
	}
	total := record.CompletedAt.Sub(ended.EndedAt).Seconds()
	return total, record.PostSuiteTotalSeconds == total
}
