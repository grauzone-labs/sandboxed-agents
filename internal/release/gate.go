package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// StableGateEvidence contains the observations used to decide whether a stable
// release may be published. Records are keyed by their prerelease asset names.
type StableGateEvidence struct {
	Commit    string           `json:"commit"`
	Preview   *PreviewEvidence `json:"preview"`
	CICommit  string           `json:"ci_commit"`
	CIGreen   bool             `json:"ci_green"`
	AssetHash string           `json:"asset_hash"`
}

// PreviewEvidence describes a published prerelease and its validation records.
type PreviewEvidence struct {
	Commit    string            `json:"commit"`
	Records   map[string][]byte `json:"records"`
	AssetHash string            `json:"asset_hash"`
}

// ValidationRecordFilenames returns the exact prerelease asset names required
// by the gate. Each call returns a new slice, so callers cannot change the policy.
func ValidationRecordFilenames() []string {
	return []string{"live-suite-linux.json", "live-suite-windows-11.json", "manual-checklist.json"}
}

// EvaluateStableGate refuses publication unless the preview validates the release.
// It reads only supplied evidence, never the network or the host environment.
func EvaluateStableGate(evidence StableGateEvidence) error {
	if evidence.Preview == nil {
		return fmt.Errorf("prerelease: no published preview on commit %s", evidence.Commit)
	}
	if evidence.Preview.Commit != evidence.Commit {
		return fmt.Errorf("prerelease: preview commit differs from release commit")
	}
	for _, filename := range ValidationRecordFilenames() {
		data, ok := evidence.Preview.Records[filename]
		if !ok {
			return fmt.Errorf("%s: validation file missing from prerelease", filename)
		}
		if err := validateRecord(data, filename, evidence.Commit); err != nil {
			return fmt.Errorf("%s: %w", filename, err)
		}
	}
	if !evidence.CIGreen || evidence.CICommit != evidence.Commit {
		return fmt.Errorf("CI: offline suite is not green on the release commit")
	}
	if !assetSHA.MatchString(evidence.AssetHash) || evidence.AssetHash != evidence.Preview.AssetHash {
		return fmt.Errorf("asset hash: preview and stable binaries differ or hash is missing")
	}
	return nil
}

func validateRecord(data []byte, filename, commit string) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("validation record is not UTF-8")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("invalid validation JSON")
	}
	manual := filename == "manual-checklist.json"
	expected := []string{"schema_version", "kind", "commit", "result", "checks"}
	if !manual {
		expected = append(expected, "platform", "image_part_selected", "image_part_ran", "image_coverage_complete")
	}
	if !exactFields(fields, expected) {
		return fmt.Errorf("validation fields are missing or additional")
	}
	var schema int
	if json.Unmarshal(fields["schema_version"], &schema) != nil || (manual && schema != 1) || (!manual && schema != 2) {
		return fmt.Errorf("unsupported schema_version")
	}
	kind := "live-suite"
	if manual {
		kind = "manual-checklist"
	}
	if !jsonString(fields["kind"], kind) {
		return fmt.Errorf("wrong validation kind")
	}
	if !commitSHA.MatchString(commit) || !jsonString(fields["commit"], commit) {
		return fmt.Errorf("validation commit differs from release commit")
	}
	if !jsonString(fields["result"], "pass") {
		return fmt.Errorf("validation gate did not pass")
	}
	if !manual {
		platform := strings.TrimSuffix(strings.TrimPrefix(filename, "live-suite-"), ".json")
		if !jsonString(fields["platform"], platform) {
			return fmt.Errorf("platform does not match filename")
		}
		var selected bool
		if bytes.Equal(fields["image_part_selected"], []byte("null")) || json.Unmarshal(fields["image_part_selected"], &selected) != nil {
			return fmt.Errorf("image_part_selected must be a boolean")
		}
		for _, field := range []string{"image_part_ran", "image_coverage_complete"} {
			if !bytes.Equal(fields[field], []byte("true")) {
				return fmt.Errorf("image coverage did not run and complete")
			}
		}
	}
	var checks []map[string]json.RawMessage
	if bytes.Equal(fields["checks"], []byte("null")) || json.Unmarshal(fields["checks"], &checks) != nil {
		return fmt.Errorf("checks must be an array of objects")
	}
	for _, check := range checks {
		expectedCheck := []string{"name", "result"}
		if manual {
			expectedCheck = append(expectedCheck, "date", "platform")
		}
		if !exactFields(check, expectedCheck) {
			return fmt.Errorf("check fields are missing or additional")
		}
		var name, result string
		if json.Unmarshal(check["name"], &name) != nil || name == "" {
			return fmt.Errorf("check name is missing")
		}
		if json.Unmarshal(check["result"], &result) != nil || (result != "pass" && result != "fail" && (manual || result != "not-run")) {
			return fmt.Errorf("invalid check result")
		}
		if manual {
			var date string
			if json.Unmarshal(check["date"], &date) != nil {
				return fmt.Errorf("manual check date is invalid")
			}
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return fmt.Errorf("manual check date is invalid")
			}
			if !jsonString(check["platform"], "linux") && !jsonString(check["platform"], "windows-11") {
				return fmt.Errorf("manual check platform is invalid")
			}
		}
	}
	return nil
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var assetSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func exactFields(fields map[string]json.RawMessage, expected []string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, field := range expected {
		if _, ok := fields[field]; !ok {
			return false
		}
	}
	return true
}

func jsonString(raw json.RawMessage, want string) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && value == want
}
