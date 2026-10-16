package release

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LiveSuiteCheck is one fixed-name check of a live-suite run. The name is a
// constant chosen by the suite, never a value taken from the host or the user.
type LiveSuiteCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

// LiveSuiteRecord is the validation record of one live-suite run on one
// platform. It holds only what the stable release gate reads.
type LiveSuiteRecord struct {
	Commit                string
	Platform              string
	Result                string
	ImagePartSelected     bool
	ImagePartRan          bool
	ImageCoverageComplete bool
	Checks                []LiveSuiteCheck
}

// liveSuiteRecordJSON fixes the field set and order of the written object.
type liveSuiteRecordJSON struct {
	SchemaVersion         int              `json:"schema_version"`
	Kind                  string           `json:"kind"`
	Commit                string           `json:"commit"`
	Platform              string           `json:"platform"`
	Result                string           `json:"result"`
	ImagePartSelected     bool             `json:"image_part_selected"`
	ImagePartRan          bool             `json:"image_part_ran"`
	ImageCoverageComplete bool             `json:"image_coverage_complete"`
	Checks                []LiveSuiteCheck `json:"checks"`
}

// LiveSuiteRecordFilename returns the prerelease asset name of a platform's record.
func LiveSuiteRecordFilename(platform string) (string, error) {
	switch platform {
	case "linux", "windows-11":
		return "live-suite-" + platform + ".json", nil
	}
	return "", fmt.Errorf("live-suite platform must be linux or windows-11")
}

// EncodeLiveSuiteRecord renders a record as the JSON object the stable release
// gate reads, with schema_version 2 and kind live-suite, refusing values
// outside the gate's vocabulary.
func EncodeLiveSuiteRecord(record LiveSuiteRecord) ([]byte, error) {
	if _, err := LiveSuiteRecordFilename(record.Platform); err != nil {
		return nil, err
	}
	if !commitSHA.MatchString(record.Commit) {
		return nil, fmt.Errorf("live-suite commit must be 40 lowercase hexadecimal characters")
	}
	if record.Result != "pass" && record.Result != "fail" {
		return nil, fmt.Errorf("live-suite result must be pass or fail")
	}
	if len(record.Checks) == 0 {
		return nil, fmt.Errorf("live-suite record needs at least one check")
	}
	seen := map[string]bool{}
	for _, check := range record.Checks {
		if check.Name == "" || seen[check.Name] {
			return nil, fmt.Errorf("live-suite check names must be unique and non-empty")
		}
		seen[check.Name] = true
		if check.Result != "pass" && check.Result != "fail" && check.Result != "not-run" {
			return nil, fmt.Errorf("live-suite check %s: result must be pass, fail, or not-run", check.Name)
		}
	}
	data, err := json.Marshal(liveSuiteRecordJSON{
		SchemaVersion: 2, Kind: "live-suite", Commit: record.Commit, Platform: record.Platform, Result: record.Result,
		ImagePartSelected: record.ImagePartSelected, ImagePartRan: record.ImagePartRan, ImageCoverageComplete: record.ImageCoverageComplete,
		Checks: record.Checks,
	})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteLiveSuiteRecord replaces the platform's record file in directory
// atomically: the record is written to a temporary file beside it and renamed
// over the previous one, so a reader sees either the old or the new record.
func WriteLiveSuiteRecord(directory string, record LiveSuiteRecord) (string, error) {
	data, err := EncodeLiveSuiteRecord(record)
	if err != nil {
		return "", err
	}
	filename, err := LiveSuiteRecordFilename(record.Platform)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, filename)
	temporary, err := os.CreateTemp(directory, "."+filename+".*")
	if err != nil {
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return "", err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporary.Name())
		return "", err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		os.Remove(temporary.Name())
		return "", err
	}
	return path, nil
}
