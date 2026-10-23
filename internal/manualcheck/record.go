// Package manualcheck runs the mechanical parts of the manual checklist in docs/manual-checklist.md
// on Linux and Windows from one implementation, records what each step observed, and keeps the
// record of the run. It never records a pass: a pass is what a person observed, and only
// AddCheck with the human-observed flag writes one.
package manualcheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Identifiers are the 25 check identifiers of the checklist, in the order of its table.
var Identifiers = []string{
	"agent-login-copilot",
	"agent-login-claude",
	"agent-login-codex",
	"agent-login-opencode",
	"github-login",
	"azure-login-device-code",
	"azure-login-browser",
	"azdo-token",
	"t3-connect-link",
	"t3-qr-pair",
	"vscode-remote-ssh",
	"npm-install-clean",
	"nuget-install-clean",
	"quick-guide-linux",
	"quick-guide-windows",
	"selection-pins-persist",
	"git-identity-persist",
	"credentials-persist",
	"update-complete",
	"update-interrupted",
	"azure-toolchain-required",
	"login-needs-terminal",
	"no-image-removal",
	"installers-no-podman",
	"preview-attestation",
}

// Platforms are the record's platform values.
const (
	PlatformLinux   = "linux"
	PlatformWindows = "windows-11"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	datePattern   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// Check is one entry of the record: the fixed identifier, the result, the day, and the platform.
type Check struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Date     string `json:"date"`
	Platform string `json:"platform"`
}

// Record is the manual-checklist record: one file for one run on one preview commit.
type Record struct {
	SchemaVersion int     `json:"schema_version"`
	Kind          string  `json:"kind"`
	Commit        string  `json:"commit"`
	Result        string  `json:"result"`
	Checks        []Check `json:"checks"`
}

// PlatformOf returns the fixed platform of an identifier, or an empty string when either platform is allowed.
func PlatformOf(name string) string {
	switch name {
	case "nuget-install-clean", "installers-no-podman", "quick-guide-windows":
		return PlatformWindows
	case "quick-guide-linux":
		return PlatformLinux
	}
	return ""
}

// NewRecord creates the record of a run in progress for a preview commit: result fail and no checks.
func NewRecord(commit string) (*Record, error) {
	if !commitPattern.MatchString(commit) {
		return nil, errors.New("commit must be 40 lowercase hexadecimal characters")
	}
	return &Record{SchemaVersion: 1, Kind: "manual-checklist", Commit: commit, Result: "fail", Checks: []Check{}}, nil
}

// LoadRecord reads a record file and rejects any field the schema does not name.
func LoadRecord(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("record is not a JSON object: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"checks", "commit", "kind", "result", "schema_version"}) {
		return nil, fmt.Errorf("record has fields %v; the schema names exactly checks, commit, kind, result, schema_version", keys)
	}
	var checks []map[string]json.RawMessage
	if err := json.Unmarshal(raw["checks"], &checks); err != nil {
		return nil, fmt.Errorf("checks is not an array: %w", err)
	}
	for index, check := range checks {
		keys = keys[:0]
		for key := range check {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"date", "name", "platform", "result"}) {
			return nil, fmt.Errorf("check %d has fields %v; a check has exactly date, name, platform, result", index, keys)
		}
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// Save writes the record atomically as private UTF-8 JSON.
func (r *Record) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manual-checklist.*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0600); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Validate applies the rules of the checklist's "The record" section: the exact fields, a lowercase
// 40-character commit, pass or fail at both levels, only the 25 identifiers each at most once,
// the fixed platform of the platform-bound identifiers, fail whenever a check failed, and for a
// pass all 25 identifiers present and passed. A record in progress with no checks is invalid, as
// the documented jq check says.
func (r *Record) Validate() error {
	if r.SchemaVersion != 1 || r.Kind != "manual-checklist" {
		return errors.New("schema_version must be 1 and kind manual-checklist")
	}
	if !commitPattern.MatchString(r.Commit) {
		return errors.New("commit must be 40 lowercase hexadecimal characters")
	}
	if r.Result != "pass" && r.Result != "fail" {
		return errors.New("result must be pass or fail")
	}
	if len(r.Checks) == 0 {
		return errors.New("the record has no checks yet")
	}
	seen := map[string]bool{}
	failed := false
	for _, check := range r.Checks {
		if !slices.Contains(Identifiers, check.Name) {
			return fmt.Errorf("unknown identifier %q", check.Name)
		}
		if seen[check.Name] {
			return fmt.Errorf("identifier %q appears twice", check.Name)
		}
		seen[check.Name] = true
		if check.Result != "pass" && check.Result != "fail" {
			return fmt.Errorf("%s: result must be pass or fail", check.Name)
		}
		if check.Platform != PlatformLinux && check.Platform != PlatformWindows {
			return fmt.Errorf("%s: platform must be linux or windows-11", check.Name)
		}
		if fixed := PlatformOf(check.Name); fixed != "" && fixed != check.Platform {
			return fmt.Errorf("%s runs only on %s", check.Name, fixed)
		}
		if !datePattern.MatchString(check.Date) {
			return fmt.Errorf("%s: date must be YYYY-MM-DD", check.Name)
		}
		failed = failed || check.Result == "fail"
	}
	if failed && r.Result != "fail" {
		return errors.New("result must be fail when a check failed")
	}
	if r.Result == "pass" && (len(seen) != len(Identifiers) || failed) {
		return errors.New("result pass needs all 25 identifiers present and passed")
	}
	return nil
}

// AddCheck records one check a person ran. A pass needs humanObserved, because no runner output is
// a manual observation. An identifier already recorded needs replace, for a rerun. The top-level
// result becomes pass only when all 25 identifiers are present and passed.
func (r *Record) AddCheck(check Check, replace, humanObserved bool) error {
	if !slices.Contains(Identifiers, check.Name) {
		return fmt.Errorf("unknown identifier %q", check.Name)
	}
	if check.Result == "pass" && !humanObserved {
		return errors.New("a pass is recorded only for what a person observed; confirm with the human-observed flag")
	}
	if check.Result != "pass" && check.Result != "fail" {
		return errors.New("result must be pass or fail")
	}
	if !datePattern.MatchString(check.Date) {
		return errors.New("date must be YYYY-MM-DD")
	}
	if check.Platform != PlatformLinux && check.Platform != PlatformWindows {
		return errors.New("platform must be linux or windows-11")
	}
	if fixed := PlatformOf(check.Name); fixed != "" && fixed != check.Platform {
		return fmt.Errorf("%s runs only on %s", check.Name, fixed)
	}
	index := slices.IndexFunc(r.Checks, func(existing Check) bool { return existing.Name == check.Name })
	if index >= 0 && !replace {
		return fmt.Errorf("check %s is already recorded; use replace to record a rerun", check.Name)
	}
	if index >= 0 {
		r.Checks = slices.Delete(r.Checks, index, index+1)
	}
	r.Checks = append(r.Checks, check)
	r.Result = "fail"
	if len(r.Checks) == len(Identifiers) && !slices.ContainsFunc(r.Checks, func(c Check) bool { return c.Result != "pass" }) {
		r.Result = "pass"
	}
	return r.Validate()
}

// Status lists every identifier with its recorded result or "not run", followed by the counts.
func (r *Record) Status() string {
	var lines []string
	lines = append(lines, "commit "+r.Commit, "result "+r.Result)
	missing, failed := 0, 0
	for _, name := range Identifiers {
		index := slices.IndexFunc(r.Checks, func(check Check) bool { return check.Name == name })
		if index < 0 {
			lines = append(lines, fmt.Sprintf("%-26s not run", name))
			missing++
			continue
		}
		check := r.Checks[index]
		if check.Result == "fail" {
			failed++
		}
		lines = append(lines, fmt.Sprintf("%-26s %s %s %s", name, check.Result, check.Date, check.Platform))
	}
	lines = append(lines, fmt.Sprintf("recorded %d of %d identifiers; %d not run; %d failed", len(r.Checks), len(Identifiers), missing, failed))
	return strings.Join(lines, "\n") + "\n"
}
