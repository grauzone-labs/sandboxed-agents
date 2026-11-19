package validationduration

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// ScopeTiming distinguishes a returned scope from an open or unentered call.
type ScopeTiming = preflighttiming.Phase

// ScopeTotals sums only closed observations, independently for each scope.
type ScopeTotals struct {
	Measured   int      `json:"measured"`
	Pass       int      `json:"pass"`
	Fail       int      `json:"fail"`
	Open       int      `json:"open"`
	NotEntered int      `json:"not_entered"`
	Seconds    *float64 `json:"seconds,omitempty"`
}

// PreflightRow correlates sanitized launch facts with two disjoint call scopes.
// Sequence zero denotes an observation whose launch record was lost.
type PreflightRow struct {
	Sequence        int         `json:"sequence"`
	Case            string      `json:"case,omitempty"`
	Operation       string      `json:"operation,omitempty"`
	Result          string      `json:"result,omitempty"`
	Seconds         *float64    `json:"seconds,omitempty"`
	Preflight       ScopeTiming `json:"preflight"`
	TargetSelection ScopeTiming `json:"target_selection"`
}

// PreflightSummary retains partial observations without claiming complete measurement.
// LedgerUnverified is sticky because an entire final launch may have been lost.
type PreflightSummary struct {
	State            string         `json:"state"`
	Reasons          []string       `json:"reasons"`
	Invocations      int            `json:"invocations"`
	Unobserved       int            `json:"unobserved"`
	Unknown          int            `json:"unknown"`
	Unattributed     int            `json:"unattributed"`
	InvalidFiles     int            `json:"invalid_files"`
	LedgerUnverified bool           `json:"ledger_unverified,omitempty"`
	Preflight        ScopeTotals    `json:"preflight"`
	TargetSelection  ScopeTotals    `json:"target_selection"`
	Rows             []PreflightRow `json:"rows"`
}

// preflightReasons fixes the only publishable incompleteness vocabulary and order.
var preflightReasons = []string{"channel-unavailable", "manifest-invalid", "revision-mismatch", "no-invocations", "suite-unfinished", "open-scope", "unknown-dispatch", "unobserved-invocation", "unattributed-record", "invalid-file"}

// classifyScope retains absence as a classification, never a fabricated zero duration.
func classifyScope(invocation *preflighttiming.Invocation, phase, other *preflighttiming.Phase) ScopeTiming {
	if invocation == nil {
		return ScopeTiming{State: preflighttiming.StateUnobserved}
	}
	if phase != nil {
		return ScopeTiming{State: phase.State, Outcome: phase.Outcome, Seconds: phase.Seconds}
	}
	if invocation.Returned || other != nil {
		return ScopeTiming{State: preflighttiming.StateNotEntered}
	}
	return ScopeTiming{State: preflighttiming.StateUnknown}
}

// CollectPreflight reads the bounded private channel and publishes only fixed vocabulary.
// Channel failures are evidence states and cannot change the original suite verdict.
func CollectPreflight(directory, revision string, suiteSeconds *float64, ledgerUnverified bool) *PreflightSummary {
	summary := &PreflightSummary{Rows: []PreflightRow{}, LedgerUnverified: ledgerUnverified}
	journal, err := preflighttiming.Journal{}, preflighttiming.ErrUnavailable
	// A failed fresh Create must never reactivate a journal left at the fixed path.
	if filepath.IsAbs(directory) {
		_, markerErr := os.Lstat(filepath.Join(filepath.Dir(directory), "preflight-timing-unverified"))
		if errors.Is(markerErr, os.ErrNotExist) {
			journal, err = preflighttiming.Read(directory, revision)
		}
	}
	channelFailure := ""
	if err != nil {
		channelFailure = "channel-unavailable"
		if errors.Is(err, preflighttiming.ErrManifest) {
			channelFailure = "manifest-invalid"
		}
		if errors.Is(err, preflighttiming.ErrRevision) {
			channelFailure = "revision-mismatch"
		}
	}
	summary.InvalidFiles = journal.InvalidFiles
	for _, entry := range journal.Entries {
		row := PreflightRow{}
		if launch := entry.Launch; launch != nil {
			if !slices.Contains(operationCases, launch.Case) || !slices.Contains(operationClasses, launch.Operation) {
				summary.InvalidFiles++
				continue
			}
			row.Sequence, row.Case, row.Operation = launch.Sequence, launch.Case, launch.Operation
			row.Result, row.Seconds = launch.Result, launch.Seconds
		}
		var p, s *preflighttiming.Phase
		if entry.Invocation != nil {
			p, s = entry.Invocation.Preflight, entry.Invocation.TargetSelection
		}
		row.Preflight = classifyScope(entry.Invocation, p, s)
		row.TargetSelection = classifyScope(entry.Invocation, s, p)
		summary.Rows = append(summary.Rows, row)
	}
	summarizePreflight(summary, suiteSeconds, channelFailure)
	return summary
}

// addScope counts one scope without treating an open or absent interval as zero.
func addScope(total *ScopeTotals, scope ScopeTiming) {
	switch scope.State {
	case preflighttiming.StateClosed:
		total.Measured++
		if scope.Outcome == preflighttiming.OutcomePass {
			total.Pass++
		} else {
			total.Fail++
		}
		if total.Seconds == nil {
			total.Seconds = new(float64)
		}
		*total.Seconds += *scope.Seconds
	case preflighttiming.StateOpen:
		total.Open++
	case preflighttiming.StateNotEntered:
		total.NotEntered++
	}
}

// summarizePreflight derives all counts, sums and reasons from retained row facts.
func summarizePreflight(summary *PreflightSummary, suiteSeconds *float64, channelFailure string) {
	summary.Invocations, summary.Unobserved, summary.Unknown, summary.Unattributed = 0, 0, 0, 0
	summary.Preflight, summary.TargetSelection = ScopeTotals{}, ScopeTotals{}
	reasons := map[string]bool{}
	if channelFailure != "" {
		reasons[channelFailure] = true
	}
	if suiteSeconds == nil {
		reasons["suite-unfinished"] = true
	}
	for _, row := range summary.Rows {
		if row.Sequence == 0 {
			summary.Unattributed++
		} else {
			summary.Invocations++
		}
		if row.Preflight.State == preflighttiming.StateUnobserved || row.TargetSelection.State == preflighttiming.StateUnobserved {
			summary.Unobserved++
		}
		if row.Preflight.State == preflighttiming.StateUnknown || row.TargetSelection.State == preflighttiming.StateUnknown || row.Sequence > 0 && row.Result == "" {
			summary.Unknown++
		}
		addScope(&summary.Preflight, row.Preflight)
		addScope(&summary.TargetSelection, row.TargetSelection)
	}
	if summary.Invocations == 0 {
		reasons["no-invocations"] = true
	}
	if summary.Preflight.Open+summary.TargetSelection.Open > 0 {
		reasons["open-scope"] = true
	}
	if summary.Unknown > 0 {
		reasons["unknown-dispatch"] = true
	}
	if summary.Unobserved > 0 {
		reasons["unobserved-invocation"] = true
	}
	if summary.Unattributed > 0 {
		reasons["unattributed-record"] = true
	}
	if summary.InvalidFiles > 0 || summary.LedgerUnverified {
		reasons["invalid-file"] = true
	}
	summary.Reasons = []string{}
	for _, reason := range preflightReasons {
		if reasons[reason] {
			summary.Reasons = append(summary.Reasons, reason)
		}
	}
	summary.State = "complete"
	if len(summary.Reasons) > 0 {
		summary.State = "incomplete"
	}
	if summary.Invocations == 0 || channelFailure == "channel-unavailable" {
		summary.State = "not-collected"
	}
	if channelFailure == "manifest-invalid" || channelFailure == "revision-mismatch" {
		summary.State = "invalid"
	}
}

// validateScope permits seconds only for an actually returned, classified scope.
func validateScope(scope ScopeTiming) bool {
	if scope.State == preflighttiming.StateClosed {
		return scope.Outcome != "" && preflighttiming.ValidCompletion(scope.Outcome, scope.Seconds)
	}
	return slices.Contains([]preflighttiming.State{preflighttiming.StateOpen, preflighttiming.StateNotEntered, preflighttiming.StateUnknown, preflighttiming.StateUnobserved}, scope.State) && scope.Outcome == "" && scope.Seconds == nil
}

// validatePreflight checks rows and independently reconciles the published summary.
func validatePreflight(summary *PreflightSummary, suiteSeconds *float64) error {
	if summary == nil {
		return nil
	}
	if len(summary.Rows) > preflighttiming.EntryLimit || summary.InvalidFiles < 0 {
		return fmt.Errorf("invalid preflight evidence bounds")
	}
	seen := map[int]bool{}
	for _, row := range summary.Rows {
		if row.Sequence < 0 || row.Sequence > preflighttiming.EntryLimit || !validateScope(row.Preflight) || !validateScope(row.TargetSelection) {
			return fmt.Errorf("invalid preflight scope")
		}
		if row.Sequence == 0 {
			if row.Case != "" || row.Operation != "" || row.Result != "" || row.Seconds != nil {
				return fmt.Errorf("invalid unattributed observation")
			}
		} else {
			if seen[row.Sequence] || !slices.Contains(operationCases, row.Case) || !slices.Contains(operationClasses, row.Operation) || !preflighttiming.ValidCompletion(row.Result, row.Seconds) {
				return fmt.Errorf("invalid preflight launch")
			}
			seen[row.Sequence] = true
		}
	}
	channelFailure := ""
	for _, reason := range summary.Reasons {
		if !slices.Contains(preflightReasons, reason) {
			return fmt.Errorf("invalid preflight reason")
		}
		if channelReason(reason) {
			if channelFailure != "" || len(summary.Rows) != 0 {
				return fmt.Errorf("invalid preflight channel state")
			}
			channelFailure = reason
		}
	}
	expected := *summary
	summarizePreflight(&expected, suiteSeconds, channelFailure)
	if !sameMeasuredSeconds(summary.Preflight.Seconds, expected.Preflight.Seconds) || !sameMeasuredSeconds(summary.TargetSelection.Seconds, expected.TargetSelection.Seconds) {
		return fmt.Errorf("inconsistent preflight seconds")
	}
	expected.Preflight.Seconds = summary.Preflight.Seconds
	expected.TargetSelection.Seconds = summary.TargetSelection.Seconds
	actualData, actualErr := json.Marshal(summary)
	expectedData, expectedErr := json.Marshal(&expected)
	if actualErr != nil || expectedErr != nil || string(actualData) != string(expectedData) {
		return fmt.Errorf("inconsistent preflight summary")
	}
	return nil
}

// sameMeasuredSeconds permits only finite row-sum rounding, preserving absence.
func sameMeasuredSeconds(actual, expected *float64) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return validSeconds(*actual) && validSeconds(*expected) && math.Abs(*actual-*expected) <= 1e-9*max(1, math.Abs(*expected))
}

// channelReason names directory/manifest failures independently of reason order.
func channelReason(reason string) bool {
	switch reason {
	case "channel-unavailable", "manifest-invalid", "revision-mismatch":
		return true
	}
	return false
}

// recollectPreflight owns sticky observation loss and suite refusal when replacing
// a previous summary; only fresh journal data is recomputed, never core evidence.
func recollectPreflight(evidence *Operations, directory string) {
	lost := evidence.Preflight != nil && evidence.Preflight.LedgerUnverified
	if evidence.Preflight != nil && slices.Contains(evidence.Preflight.Reasons, "channel-unavailable") {
		directory = ""
	}
	evidence.Preflight = CollectPreflight(directory, evidence.Commit, evidence.SuiteSeconds, lost)
}
