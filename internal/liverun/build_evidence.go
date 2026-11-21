package liverun

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildEvidenceEnv enables the private host-only iidfile channel for an authorized live invocation.
const BuildEvidenceEnv = "SANDBOXED_AGENTS_LIVE_BUILD_EVIDENCE_DIR"

// buildManifest binds a private producer directory to one revision, engine and unpredictable invocation identity.
type buildManifest struct {
	Version                        int
	Invocation, Revision, EngineID string
}

// buildPublication carries a manifest-bound final, attempt, retention-only input, or unconfirmed-build observation independently of output text.
type buildPublication struct {
	Version                        int
	Invocation, Revision, EngineID string
	Result                         BuildResult
	Input                          *registryBuildInput `json:",omitempty"`
	Unconfirmed                    bool                `json:",omitempty"`
}

// Build event prefixes agree between producers and the collector; manifest-loss markers carry negative evidence only.
const (
	buildAttemptPrefix     = "attempt-"
	buildResultPrefix      = "result-"
	buildInputPrefix       = "input-"
	buildUnconfirmedPrefix = "unconfirmed-"
	buildReadLossPrefix    = "read-loss-"
	buildInvalidPrefix     = "invalid-manifest-"
)

// bind constructs a publication with this manifest's exact invocation, revision and engine binding.
func (m buildManifest) bind(version int) buildPublication {
	return buildPublication{Version: version, Invocation: m.Invocation, Revision: m.Revision, EngineID: m.EngineID}
}

// bound checks the complete manifest binding independently of event version, payload or filename kind.
func (m buildManifest) bound(event buildPublication) bool {
	return event.Invocation == m.Invocation && event.Revision == m.Revision && event.EngineID == m.EngineID
}

// createBuildChannel creates the private per-invocation producer channel before the suite starts.
func createBuildChannel(o Options, before Snapshot) (string, error) {
	directory := filepath.Join(o.RunDir, "build-evidence")
	if within(o.Checkout, directory) {
		return "", fmt.Errorf("build evidence must be outside checkout")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return directory, writeJSON(filepath.Join(directory, "manifest.json"), buildManifest{Version: 2, Invocation: hex.EncodeToString(nonce), Revision: o.Revision, EngineID: before.EngineID})
}

// strictBuildJSON decodes one bounded typed artifact and rejects trailing or unknown fields.
func strictBuildJSON(path string, value any) error {
	data, err := cleanupRead(path, 64*1024)
	if err != nil {
		return err
	}
	return strictDecode(data, value)
}

// strictDecode decodes exactly one typed JSON value and rejects duplicate members and trailing or unknown fields.
func strictDecode(data []byte, value any) error {
	if err := rejectDuplicateMembers(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("extra build evidence data")
	}
	return nil
}

// duplicateFrame tracks one open JSON object or array while rejectDuplicateMembers scans its first value.
type duplicateFrame struct {
	object    bool
	expectKey bool
	seen      map[string]bool
}

// rejectDuplicateMembers scans the first JSON value in data without recursion and refuses any object that names a
// member twice. Names compare case-insensitively, as encoding/json matches struct fields, so a case variant cannot
// silently replace an earlier member either.
func rejectDuplicateMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	stack := []*duplicateFrame{}
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		var top *duplicateFrame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if delim, ok := token.(json.Delim); ok && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
		} else if top != nil && top.object && top.expectKey {
			name := strings.ToLower(token.(string))
			if top.seen[name] {
				return fmt.Errorf("duplicate member in private evidence")
			}
			top.seen[name] = true
			top.expectKey = false
			continue
		} else {
			if top != nil && top.object {
				top.expectKey = true
			}
			if delim, ok := token.(json.Delim); ok {
				stack = append(stack, &duplicateFrame{object: delim == '{', expectKey: delim == '{', seen: map[string]bool{}})
			}
		}
		if len(stack) == 0 {
			return nil
		}
	}
}

// readBuildManifest validates the private channel's version and immutable invocation metadata.
func readBuildManifest(directory string) (buildManifest, error) {
	var m buildManifest
	if !filepath.IsAbs(directory) {
		return m, fmt.Errorf("build channel must be absolute")
	}
	err := strictBuildJSON(filepath.Join(directory, "manifest.json"), &m)
	if err != nil {
		return m, fmt.Errorf("build channel manifest unverified: %w", err)
	}
	if (m.Version != 1 && m.Version != 2) || !revisionLine.MatchString(m.Revision) || len(m.Invocation) != 64 || m.EngineID == "" {
		return m, fmt.Errorf("build channel manifest unverified")
	}
	if _, err = hex.DecodeString(m.Invocation); err != nil {
		return m, fmt.Errorf("build invocation unverified")
	}
	return m, nil
}

// buildFinalSource identifies a builder result confirmed independently under the image-tag lock.
const buildFinalSource = "podman --iidfile + image inspect under tag lock"

// SingleCommitPolicy names the selected layer-free, single-stage build accounting policy.
const SingleCommitPolicy = "single-commit"

// CaptureBuildResult preserves the legacy iidfile-only API without asserting single-commit completeness or excluding reuse.
func CaptureBuildResult(directory, tag, asset string, build func(string) error, inspect func(string) (string, error)) error {
	return captureBuildResult(directory, tag, asset, "", true, build, inspect)
}

// CaptureSingleCommitBuild records the confirmed final of a caller's layer-free single-stage build while preserving its original outcome.
func CaptureSingleCommitBuild(directory, tag, asset string, reuseNotExcluded bool, build func(string) error, inspect func(string) (string, error)) error {
	return captureBuildResult(directory, tag, asset, SingleCommitPolicy, reuseNotExcluded, build, inspect)
}

// captureBuildResult publishes an attempt before execution and closes it only after iid and tag inspection agree.
// The caller holds the tag lock, supplies the declared build policy, and confirms the selected engine in inspect.
// Artifacts are supplemental: every path returns the original build error rather than a telemetry failure.
func captureBuildResult(directory, tag, asset, policy string, reuse bool, build func(string) error, inspect func(string) (string, error)) error {
	m, err := readBuildManifest(directory)
	if directory == "" || err != nil {
		markBuildManifestLoss(directory, err)
		return build("")
	}
	if len(asset) != 64 || !cleanupProjectTag(tag) || !strings.HasSuffix(tag, "-"+asset) {
		_ = RecordUnconfirmedBuild(directory)
		return build("")
	}
	attemptID, err := newBuildAttemptID()
	if err != nil {
		markBuildChannelUnverified(directory)
		return build("")
	}
	private, err := os.MkdirTemp(directory, "iid-")
	if err != nil {
		markBuildChannelUnverified(directory)
		return build("")
	}
	defer os.RemoveAll(private)
	result := BuildResult{Asset: asset, Step: "COMMIT " + tag, Source: buildFinalSource, AttemptID: attemptID, Policy: policy, Reproducibility: "default-timestamps", PublishedAt: time.Now().UTC()}
	if reuse {
		result.Reproducibility = "reuse-not-excluded"
	}
	version := 2
	if policy == "" {
		version = 1
	}
	attempt := m.bind(version)
	attempt.Result = result
	attemptPath := filepath.Join(directory, buildAttemptPrefix+attemptID+".json")
	if err = writeJSON(attemptPath, attempt); err != nil {
		markBuildChannelUnverified(directory)
		return build("")
	}
	iid := filepath.Join(private, "result.id")
	buildErr := build(iid)
	result.EndedAt = time.Now().UTC()
	result.Outcome = "committed"
	if buildErr != nil {
		result.Outcome = "committed-build-error"
	}
	data, readErr := cleanupRead(iid, 128)
	id := canonicalImageID(strings.TrimSpace(string(data)))
	if readErr == nil && fullImageID(id) {
		confirmed, inspectErr := inspect(tag)
		if inspectErr == nil && canonicalImageID(confirmed) == id {
			result.ID = id
			attempt.Result = result
			if publishBuildEvent(directory, buildResultPrefix, attempt) == nil {
				_ = os.Remove(attemptPath)
			}
		}
	}
	if version == 1 && buildErr != nil {
		_ = os.Remove(attemptPath)
	}
	return buildErr
}

// buildChannelState separates result authority, open attempts and accounting completeness from malformed evidence.
type buildChannelState struct {
	Inputs            map[string]bool
	Results           []BuildResult
	Complete          bool
	SingleCommitKnown bool
	LegacyKnown       bool
	Open              bool
	Err               error
}

// readBuildResults preserves the legacy reader seam while production also consumes explicit accounting state.
func readBuildResults(o Options, before Snapshot) ([]BuildResult, error) {
	state := readBuildChannel(o, before)
	return state.Results, state.Err
}

// readBuildChannel validates each independent event and never promotes a legacy receipt to single-commit authority.
func readBuildChannel(o Options, before Snapshot) (state buildChannelState) {
	defer func() {
		if state.Err != nil {
			state.Complete = false
			state.Open = true
		}
	}()
	directory := filepath.Join(o.RunDir, "build-evidence")
	if _, err := os.Stat(filepath.Join(o.RunDir, "build-evidence-unverified")); !os.IsNotExist(err) {
		state.Err = fmt.Errorf("build channel publication unavailable")
	}
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return state
	}
	m, err := readBuildManifest(directory)
	if err != nil || m.Revision != o.Revision || m.EngineID != before.EngineID {
		state.Err = fmt.Errorf("build manifest changed")
		return state
	}
	state.Inputs = map[string]bool{}
	state.Complete = m.Version == 2
	state.SingleCommitKnown = m.Version == 2
	state.LegacyKnown = m.Version == 1
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) > 10001 {
		state.Err = fmt.Errorf("build channel unreadable or oversized")
		return state
	}
	attempts := map[string]BuildResult{}
	invalidAttempts := map[string]bool{}
	closed := map[string]BuildResult{}
	groups := map[string][]BuildResult{}
	var total int64
	invalid, incomplete := false, false
	for _, entry := range entries {
		name := entry.Name()
		if name == "manifest.json" {
			continue
		}
		info, e := entry.Info()
		if e != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > 64*1024 || !strings.HasSuffix(name, ".json") {
			invalid = true
			continue
		}
		total += info.Size()
		if total > cleanupPrivateLimit {
			state.Results = nil
			state.Err = fmt.Errorf("build channel exceeds evidence limit")
			return state
		}
		if strings.HasPrefix(name, buildReadLossPrefix) || strings.HasPrefix(name, buildInvalidPrefix) {
			incomplete = true
			continue
		}
		var event buildPublication
		isAttempt := strings.HasPrefix(name, buildAttemptPrefix)
		if strictBuildJSON(filepath.Join(directory, name), &event) != nil {
			invalid = true
			continue
		}
		if strings.HasPrefix(name, buildUnconfirmedPrefix) {
			// This observation has no image identity and can only withdraw completeness.
			if event.Version == 2 && m.bound(event) && event.Unconfirmed && event.Input == nil && event.Result == (BuildResult{}) {
				incomplete = true
			} else {
				invalid = true
			}
			continue
		}
		if strings.HasPrefix(name, buildInputPrefix) {
			if !event.Unconfirmed && event.Version == 2 && m.bound(event) && event.Input != nil && event.Result == (BuildResult{}) && validRegistryInput(*event.Input) {
				state.Inputs[event.Input.ID] = true
			} else {
				invalid = true
			}
			continue
		}
		if (!isAttempt && !strings.HasPrefix(name, buildResultPrefix)) || event.Unconfirmed || event.Input != nil || !validBuildPublication(event, m, isAttempt) {
			invalid = true
			continue
		}
		result := event.Result
		if event.Version != 2 {
			state.LegacyKnown = true
			state.Complete = false
			state.Open = true
		}
		if isAttempt {
			if invalidAttempts[result.AttemptID] || name != buildAttemptPrefix+result.AttemptID+".json" {
				invalid = true
				invalidAttempts[result.AttemptID] = true
				filenameID := strings.TrimSuffix(strings.TrimPrefix(name, buildAttemptPrefix), ".json")
				if validAttemptID(filenameID) {
					invalidAttempts[filenameID] = true
				}
				continue
			}
			attempts[result.AttemptID] = result
			continue
		}
		if event.Version == 1 {
			result.Reproducibility = "reuse-not-excluded"
			result.Policy = ""
		}
		if event.Version == 2 {
			state.SingleCommitKnown = true
			groups[result.AttemptID] = append(groups[result.AttemptID], result)
		} else {
			state.Results = append(state.Results, result)
		}
	}
	groupIDs := make([]string, 0, len(groups))
	for id := range groups {
		groupIDs = append(groupIDs, id)
	}
	sort.Strings(groupIDs)
	for _, id := range groupIDs {
		group := groups[id]
		first := group[0]
		consistent := !invalidAttempts[id]
		for _, r := range group[1:] {
			if r != first {
				consistent = false
			}
		}
		if attempt, ok := attempts[id]; ok && (first.Step != attempt.Step || first.Asset != attempt.Asset || first.Policy != attempt.Policy || first.Reproducibility != attempt.Reproducibility) {
			consistent = false
		}
		if !consistent {
			invalid = true
			continue
		}
		state.Results = append(state.Results, first)
		closed[id] = first
	}

	for id, attempt := range attempts {
		receipt, ok := closed[id]
		if !ok || receipt.Step != attempt.Step || receipt.Asset != attempt.Asset || receipt.Policy != attempt.Policy || receipt.Reproducibility != attempt.Reproducibility {
			state.Open = true
		}
	}
	if invalid || incomplete {
		state.Err = fmt.Errorf("some producer evidence incomplete or invalid")
		state.Complete = false
		state.Open = true
	}
	return state
}

// RecordUnconfirmedBuild withdraws complete accounting when a build must run without confirmed capture preconditions.
// It binds observations only after manifest validation; a failed read or validation attempts an identity-free in-channel marker.
// Bound observation publication failure falls back to the run's private marker; no path grants ownership or changes a build outcome.
func RecordUnconfirmedBuild(directory string) error {
	m, err := readBuildManifest(directory)
	if err != nil {
		markBuildManifestLoss(directory, err)
		return err
	}
	event := m.bind(2)
	event.Unconfirmed = true
	err = publishBuildEvent(directory, buildUnconfirmedPrefix, event)
	if err != nil {
		markBuildChannelUnverified(directory)
	}
	return err
}

// markBuildManifestLoss attempts an identity-free negative marker only inside the existing channel, distinguishing I/O from invalid contents.
// It never creates directories, writes a parent marker, reuses stale manifest authority, or changes a build outcome.
func markBuildManifestLoss(directory string, readErr error) {
	var pathErr *os.PathError
	if !filepath.IsAbs(directory) || readErr == nil {
		return
	}
	prefix := buildInvalidPrefix
	if errors.As(readErr, &pathErr) {
		prefix = buildReadLossPrefix
	}
	marker, err := os.CreateTemp(directory, prefix+"*.json")
	if err == nil {
		_ = marker.Close()
	}
}

// ConfirmBuildEngine binds a producer's primary engine-info query to the private invocation.
func ConfirmBuildEngine(directory, engine string) error {
	m, err := readBuildManifest(directory)
	if err != nil || m.EngineID != engine {
		return fmt.Errorf("build engine differs from invocation")
	}
	return nil
}

// publishBuildEvent atomically publishes a bounded event under a unique producer-owned filename.
func publishBuildEvent(directory, prefix string, event buildPublication) error {
	nonce, err := newBuildAttemptID()
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(directory, prefix+nonce+".json"), event)
}

// validBuildPublication admits only correctly bound typed attempts or confirmed full final results.
func validBuildPublication(event buildPublication, m buildManifest, attempt bool) bool {
	r := event.Result
	if (event.Version != 1 && event.Version != 2) || !m.bound(event) {
		return false
	}
	if !strings.HasPrefix(r.Step, "COMMIT ") || !cleanupProjectTag(strings.TrimPrefix(r.Step, "COMMIT ")) || len(r.Asset) != 64 || !strings.HasSuffix(r.Step, "-"+r.Asset) {
		return false
	}
	if event.Version == 1 {
		return !attempt && r.Source == buildFinalSource && fullImageID(r.ID)
	}
	if r.LayerInvocation != "" || r.Policy != SingleCommitPolicy || r.Source != buildFinalSource || !validAttemptID(r.AttemptID) || (r.Reproducibility != "default-timestamps" && r.Reproducibility != "reuse-not-excluded") {
		return false
	}
	if attempt {
		return r.ID == "" && r.Outcome == ""
	}
	return fullImageID(r.ID) && (r.Outcome == "committed" || r.Outcome == "committed-build-error")
}

// validAttemptID admits a complete cryptographic per-attempt identity.
func validAttemptID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// newBuildAttemptID creates an unpredictable identity independent of every other attempt.
func newBuildAttemptID() (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce), nil
}

// markBuildChannelUnverified retains startup, attempt or observation publication loss when the validated channel's parent remains writable.
func markBuildChannelUnverified(directory string) {
	_ = os.WriteFile(filepath.Join(filepath.Dir(directory), "build-evidence-unverified"), []byte("producer evidence unavailable\n"), 0600)
}

// registryBuildInput records an inspected identity for a literal registry FROM reference, never an owned commit.
type registryBuildInput struct{ Reference, ID string }

// RecordRegistryBuildInput publishes an already-known registry reference's independently inspected identity for retention-only accounting.
// The host caller confirms the selected engine and excludes product-labelled images; no pull attribution or deletion authority is created.
func RecordRegistryBuildInput(directory, reference, id string) error {
	m, err := readBuildManifest(directory)
	if err != nil {
		return err
	}
	input := registryBuildInput{Reference: reference, ID: canonicalImageID(id)}
	if !validRegistryInput(input) {
		return fmt.Errorf("registry input unverified")
	}
	event := m.bind(2)
	event.Input = &input
	return publishBuildEvent(directory, buildInputPrefix, event)
}

// validRegistryInput requires a full identity and a bounded literal non-project reference without shell or option syntax.
func validRegistryInput(input registryBuildInput) bool {
	if !fullImageID(input.ID) || input.Reference == "" || input.Reference == "scratch" || fullImageID(canonicalImageID(input.Reference)) || len(input.Reference) > 512 || strings.HasPrefix(input.Reference, "-") || strings.HasPrefix(input.Reference, "localhost/") {
		return false
	}
	for _, r := range input.Reference {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/:@-", r)) {
			return false
		}
	}
	return true
}
