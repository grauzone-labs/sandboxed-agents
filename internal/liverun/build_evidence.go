package liverun

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// buildPublication holds one primary final result or typed build attempt independently of all output text.
type buildPublication struct {
	Version                        int
	Invocation, Revision, EngineID string
	Result                         BuildResult
	Input                          *registryBuildInput `json:",omitempty"`
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("extra build evidence data")
	}
	return nil
}

// readBuildManifest validates the private channel's version and immutable invocation metadata.
func readBuildManifest(directory string) (buildManifest, error) {
	var m buildManifest
	if !filepath.IsAbs(directory) {
		return m, fmt.Errorf("build channel must be absolute")
	}
	err := strictBuildJSON(filepath.Join(directory, "manifest.json"), &m)
	if err != nil || (m.Version != 1 && m.Version != 2) || !revisionLine.MatchString(m.Revision) || len(m.Invocation) != 64 || m.EngineID == "" {
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
	if directory == "" || err != nil || len(asset) != 64 || !cleanupProjectTag(tag) || !strings.HasSuffix(tag, "-"+asset) {
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
	attempt := buildPublication{Version: version, Invocation: m.Invocation, Revision: m.Revision, EngineID: m.EngineID, Result: result}
	attemptPath := filepath.Join(directory, "attempt-"+attemptID+".json")
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
			if publishBuildEvent(directory, "result-", attempt) == nil {
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
		state.Err = fmt.Errorf("build channel failed to initialize")
		return state
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
	closed := map[string]BuildResult{}
	groups := map[string][]BuildResult{}
	var total int64
	invalid := false
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
		var event buildPublication
		isAttempt := strings.HasPrefix(name, "attempt-")
		if strictBuildJSON(filepath.Join(directory, name), &event) != nil {
			invalid = true
			continue
		}
		if strings.HasPrefix(name, "input-") {
			if event.Version == 2 && event.Invocation == m.Invocation && event.Revision == m.Revision && event.EngineID == m.EngineID && event.Input != nil && event.Result == (BuildResult{}) && validRegistryInput(*event.Input) {
				state.Inputs[event.Input.ID] = true
			} else {
				invalid = true
			}
			continue
		}
		if (!isAttempt && !strings.HasPrefix(name, "result-")) || event.Input != nil || !validBuildPublication(event, m, isAttempt) {
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
		consistent := true
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
	if invalid {
		state.Err = fmt.Errorf("some producer evidence incomplete or invalid")
		state.Complete = false
		state.Open = true
	}
	return state
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
	if (event.Version != 1 && event.Version != 2) || event.Invocation != m.Invocation || event.Revision != m.Revision || event.EngineID != m.EngineID {
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

// markBuildChannelUnverified retains startup publication loss outside the channel when its parent remains writable.
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
	return publishBuildEvent(directory, "input-", buildPublication{Version: 2, Invocation: m.Invocation, Revision: m.Revision, EngineID: m.EngineID, Input: &input})
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
