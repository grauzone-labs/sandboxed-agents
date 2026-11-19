package validationduration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
)

// Engine records the selected container engine's capacity, not the CLI host's.
type Engine struct {
	CPUs        int    `json:"cpus"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

// Operation records one inclusive process invocation without its arguments or output.
type Operation struct {
	Case      string  `json:"case"`
	Source    string  `json:"source"`
	Operation string  `json:"operation"`
	Result    string  `json:"result"`
	Seconds   float64 `json:"seconds"`
}

// OperationCase reconciles a case's inclusive duration with its process invocations.
type OperationCase struct {
	Name               string  `json:"name"`
	Result             string  `json:"result"`
	Seconds            float64 `json:"seconds"`
	UnaccountedSeconds float64 `json:"unaccounted_seconds"`
}

// Operations is supplemental evidence independent of schema 2 and the release gate.
// Negative unaccounted time is retained: overlapping invocations can exceed wall time.
type Operations struct {
	SchemaVersion      int             `json:"schema_version"`
	Kind               string          `json:"kind"`
	Commit             string          `json:"commit"`
	Platform           string          `json:"platform"`
	CaseOverlap        int             `json:"case_overlap,omitempty"`
	BuildJobs          int             `json:"build_jobs,omitempty"`
	GoTestParallel     int             `json:"go_test_parallel,omitempty"`
	Engine             *Engine         `json:"engine,omitempty"`
	Operations         []Operation     `json:"operations"`
	Cases              []OperationCase `json:"cases"`
	PreparationSeconds *float64        `json:"preparation_seconds,omitempty"`
	PostSuiteSeconds   *float64        `json:"post_suite_seconds,omitempty"`
}

// operationClasses is the fixed live-validation vocabulary, with preflight and
// inspection for invocations which do not perform a named lifecycle operation.
var operationClasses = []string{"preflight", "inspection", "image archive", "image build or sandbox start", "sandbox start or restart", "SSH server check", "smoke", "update or rollback", "agent install", "agent session", "rebuild", "cleanup", "Tool install", "Tool update", "Tool delete", "Tool probe"}

// operationCases permits only fixed suite identifiers, including outer cleanup.
var operationCases = []string{"suite", "preflight", "lifecycle", "update", "agent", "t3code", "images"}

// OperationClasses returns the authoritative fixed timing vocabulary as an independent slice.
// The ambiguous up class retains inclusive elapsed time without claiming a build was observed.
func OperationClasses() []string { return slices.Clone(operationClasses) }

// OperationCases returns the authoritative timing case identifiers as an independent slice.
func OperationCases() []string { return slices.Clone(operationCases) }

// WriteOperations validates and atomically retains only sanitized timing facts.
func WriteOperations(directory string, evidence Operations) (string, error) {
	if err := validateOperations(evidence); err != nil {
		return "", err
	}
	evidence.Cases = slices.Clone(evidence.Cases)
	for index := range evidence.Cases {
		item := &evidence.Cases[index]
		item.UnaccountedSeconds = item.Seconds
		for _, operation := range evidence.Operations {
			if operation.Case == item.Name {
				item.UnaccountedSeconds -= operation.Seconds
			}
		}
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "operations-"+evidence.Platform+".json")
	if err := writeEvidence(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// validateOperations rejects free-form strings and invalid measurements before publishing.
func validateOperations(evidence Operations) error {
	if evidence.SchemaVersion != 1 || evidence.Kind != "live-suite-operations" || !commitPattern.MatchString(evidence.Commit) || !slices.Contains([]string{"linux", "windows-11"}, evidence.Platform) {
		return fmt.Errorf("invalid operation evidence identity")
	}
	if evidence.CaseOverlap < 0 || evidence.BuildJobs < 0 || evidence.GoTestParallel < 0 {
		return fmt.Errorf("invalid configured concurrency")
	}
	if evidence.Engine != nil && (evidence.Engine.CPUs <= 0 || evidence.Engine.MemoryBytes == 0) {
		return fmt.Errorf("invalid engine capacity")
	}
	for _, seconds := range []*float64{evidence.PreparationSeconds, evidence.PostSuiteSeconds} {
		if seconds != nil && !validSeconds(*seconds) {
			return fmt.Errorf("invalid phase duration")
		}
	}
	for _, operation := range evidence.Operations {
		if !slices.Contains(operationCases, operation.Case) || !slices.Contains([]string{"cli", "podman"}, operation.Source) || !slices.Contains(operationClasses, operation.Operation) || !slices.Contains([]string{"pass", "fail"}, operation.Result) || !validSeconds(operation.Seconds) {
			return fmt.Errorf("invalid sanitized operation")
		}
	}
	seen := map[string]bool{}
	for _, item := range evidence.Cases {
		if !slices.Contains(operationCases, item.Name) || seen[item.Name] || !slices.Contains([]string{"pass", "fail", "skip"}, item.Result) || !validSeconds(item.Seconds) || math.IsNaN(item.UnaccountedSeconds) || math.IsInf(item.UnaccountedSeconds, 0) {
			return fmt.Errorf("invalid operation case")
		}
		seen[item.Name] = true
	}
	return nil
}

// validSeconds accepts only finite, nonnegative elapsed durations.
func validSeconds(seconds float64) bool {
	return seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

// writeEvidence replaces one supplemental file only after a complete private write.
func writeEvidence(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".timing-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// retainOperationPhases updates optional phases of this run's operation evidence.
// Missing evidence remains absent for older or interrupted runs, never fabricated.
func retainOperationPhases(directory string, source sourceRecord, state Context) error {
	path := filepath.Join(directory, "operations-"+source.Platform+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var evidence Operations
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return fmt.Errorf("invalid operation evidence")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid operation evidence")
	}
	if evidence.Commit != source.Commit || evidence.Platform != source.Platform {
		return fmt.Errorf("operation evidence belongs to a different run revision or platform")
	}
	if err := validateOperations(evidence); err != nil {
		return err
	}
	if state.Preparation != nil {
		evidence.PreparationSeconds, err = phaseSeconds(state.Preparation)
		if err != nil {
			return err
		}
	}
	if state.PostSuite != nil {
		evidence.PostSuiteSeconds, err = phaseSeconds(state.PostSuite)
		if err != nil {
			return err
		}
	}
	_, err = WriteOperations(directory, evidence)
	return err
}
