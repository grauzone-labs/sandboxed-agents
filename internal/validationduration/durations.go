// Package validationduration retains sanitized, supplemental live-suite timing evidence.
package validationduration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

// Context describes categorical observations made before a validation run.
// Unknown is explicit: elapsed times do not prove the initial cache or image state.
type Context struct {
	HostLoad    string    `json:"host_load"`
	BaseImage   string    `json:"base_image"`
	BuildCache  string    `json:"build_cache"`
	Preparation *Interval `json:"-"`
	PostSuite   *Interval `json:"-"`
}

// Interval supplies phase boundaries to the collector; timestamps are never retained.
type Interval struct {
	Start time.Time
	End   time.Time
}

// phaseSeconds validates optional boundaries and returns only their elapsed seconds.
func phaseSeconds(interval *Interval) (*float64, error) {
	if interval == nil {
		return nil, nil
	}
	if interval.Start.IsZero() || interval.End.IsZero() || interval.End.Before(interval.Start) {
		return nil, fmt.Errorf("phase needs ordered start and end timestamps")
	}
	seconds := interval.End.Sub(interval.Start).Seconds()
	return &seconds, nil
}

// sourceRecord selects comparison facts from the existing release record.
type sourceRecord struct {
	SchemaVersion         int    `json:"schema_version"`
	Kind                  string `json:"kind"`
	Commit                string `json:"commit"`
	Platform              string `json:"platform"`
	ImagePartSelected     bool   `json:"image_part_selected"`
	ImagePartRan          bool   `json:"image_part_ran"`
	ImageCoverageComplete bool   `json:"image_coverage_complete"`
}

// caseDuration holds a fixed case's inclusive time, never a sum of subtests.
type caseDuration struct {
	Name    string  `json:"name"`
	Result  string  `json:"result"`
	Seconds float64 `json:"seconds"`
}

// evidence holds only allowlisted comparison facts and timings, independently of the release gate.
type evidence struct {
	sourceRecord
	Context            Context        `json:"context"`
	Cases              []caseDuration `json:"cases"`
	PreparationSeconds *float64       `json:"preparation_seconds,omitempty"`
	PostSuiteSeconds   *float64       `json:"post_suite_seconds,omitempty"`
}

// completionLine accepts only immediate, fixed-name cases from Go's verbose output.
var completionLine = regexp.MustCompile(`^    --- (PASS|FAIL|SKIP): TestLiveSuite/(preflight|lifecycle|update|agent|t3code|images) \(([0-9]+\.[0-9]+)s\)$`)

// commitPattern permits only the revision form used by live-suite records.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Collect derives inclusive case durations from verbose logs and comparison context.
// Nested and unknown subtests and all program output are discarded.
func Collect(log io.Reader, record []byte, state Context) ([]byte, error) {
	var source sourceRecord
	if err := json.Unmarshal(record, &source); err != nil {
		return nil, fmt.Errorf("invalid live-suite record")
	}
	_, platformErr := release.LiveSuiteRecordFilename(source.Platform)
	if source.SchemaVersion != 2 || source.Kind != "live-suite" || !commitPattern.MatchString(source.Commit) || platformErr != nil {
		return nil, fmt.Errorf("expected schema 2 live-suite record with revision and platform")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record, &fields); err != nil {
		return nil, fmt.Errorf("invalid live-suite record")
	}
	for _, field := range []string{"image_part_selected", "image_part_ran", "image_coverage_complete"} {
		if string(fields[field]) != "true" && string(fields[field]) != "false" {
			return nil, fmt.Errorf("missing or invalid coverage state")
		}
	}
	if !slices.Contains([]string{"idle", "shared", "unknown"}, state.HostLoad) || !slices.Contains([]string{"present", "absent", "unknown"}, state.BaseImage) || !slices.Contains([]string{"warm", "cold", "unknown"}, state.BuildCache) {
		return nil, fmt.Errorf("comparison context must use categorical values")
	}
	source.SchemaVersion, source.Kind = 1, "live-suite-durations"
	result := evidence{sourceRecord: source, Context: state, Cases: []caseDuration{}}
	var err error
	result.PreparationSeconds, err = phaseSeconds(state.Preparation)
	if err != nil {
		return nil, err
	}
	result.PostSuiteSeconds, err = phaseSeconds(state.PostSuite)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	// Fixed completion lines fit in 4 KiB. Discard oversized program lines in
	// chunks so untrusted output cannot grow memory or hide later completions.
	reader := bufio.NewReaderSize(log, 4096)
	oversized := false
	for {
		line, err := reader.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			oversized = true
			continue
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if !oversized {
			match := completionLine.FindStringSubmatch(strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"))
			if match != nil {
				if seen[match[2]] {
					return nil, fmt.Errorf("repeated case: supply one verbose suite run")
				}
				seen[match[2]] = true
				seconds, parseErr := strconv.ParseFloat(match[3], 64)
				if parseErr != nil {
					return nil, parseErr
				}
				result.Cases = append(result.Cases, caseDuration{Name: match[2], Result: strings.ToLower(match[1]), Seconds: seconds})
			}
		}
		oversized = false
		if err == io.EOF {
			break
		}
	}
	return json.Marshal(result)
}

// Retain writes timing evidence beside a schema 2 record without modifying it.
func Retain(log io.Reader, recordPath string, state Context) (string, error) {
	record, err := os.ReadFile(recordPath)
	if err != nil {
		return "", err
	}
	data, err := Collect(log, record, state)
	if err != nil {
		return "", err
	}
	var source sourceRecord
	if err := json.Unmarshal(data, &source); err != nil {
		return "", err
	}
	recordName, err := release.LiveSuiteRecordFilename(source.Platform)
	if err != nil {
		return "", err
	}
	if filepath.Base(recordPath) != recordName {
		return "", fmt.Errorf("expected the platform live-suite record filename")
	}
	path := filepath.Join(filepath.Dir(recordPath), "durations-"+source.Platform+".json")
	if err := retainOperationPhases(filepath.Dir(recordPath), source, state); err != nil {
		return "", err
	}
	if err := writeEvidence(path, data); err != nil {
		return "", err
	}
	return path, nil
}
