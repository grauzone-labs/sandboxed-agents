package preflighttiming

import (
	"math"
	"time"
)

// State identifies an observed or classified scope without free-form detail.
type State string

// Scope states distinguish returned intervals, entered calls and missing evidence.
const (
	StateOpen       State = "open"
	StateClosed     State = "closed"
	StateRepeated   State = "repeated"
	StateNotEntered State = "not-entered"
	StateUnknown    State = "unknown"
	StateUnobserved State = "unobserved"
)

// OutcomePass and OutcomeFail describe only a returned nil error versus error.
const (
	OutcomePass = "pass"
	OutcomeFail = "fail"
)

// MaxSeconds is the existing documented 120-minute live-suite observation window.
// It adds no deadline of its own; the helper derives its suite timeout from it.
// With at most EntryLimit rows, either scope sum is at most 58,982,400 seconds,
// safely finite even with floating-point rounding.
const MaxSeconds = float64(120 * time.Minute / time.Second)

// ValidSeconds admits only finite nonnegative intervals within the suite window.
// This bound belongs to the private journal, not legacy core operation evidence.
func ValidSeconds(seconds float64) bool {
	return seconds >= 0 && seconds <= MaxSeconds && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

// ValidCompletion accepts either a missing result/seconds pair or a bounded
// returned outcome; an absent duration is never treated as measured zero.
func ValidCompletion(result string, seconds *float64) bool {
	if result == "" {
		return seconds == nil
	}
	return (result == OutcomePass || result == OutcomeFail) && seconds != nil && ValidSeconds(*seconds)
}

// ValidObservation accepts only an entered-open phase without completion data
// or a closed phase with a bounded returned outcome. Collector classifications
// and repeated observations are not part of this journal grammar.
func (phase Phase) ValidObservation() bool {
	switch phase.State {
	case StateOpen:
		return phase.Outcome == "" && phase.Seconds == nil
	case StateClosed:
		return phase.Outcome != "" && ValidCompletion(phase.Outcome, phase.Seconds)
	default:
		return false
	}
}
