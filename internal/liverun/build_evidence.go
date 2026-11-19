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
	"strings"
)

// BuildEvidenceEnv enables the private host-only iidfile channel for an authorized live invocation.
const BuildEvidenceEnv = "SANDBOXED_AGENTS_LIVE_BUILD_EVIDENCE_DIR"

// buildManifest binds a private producer directory to one revision, engine and unpredictable invocation identity.
type buildManifest struct {
	Version                        int
	Invocation, Revision, EngineID string
}

// buildPublication holds one builder-controlled final result independently of all output text.
type buildPublication struct {
	Version                        int
	Invocation, Revision, EngineID string
	Result                         BuildResult
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
	return directory, writeJSON(filepath.Join(directory, "manifest.json"), buildManifest{Version: 1, Invocation: hex.EncodeToString(nonce), Revision: o.Revision, EngineID: before.EngineID})
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
	if err != nil || m.Version != 1 || !revisionLine.MatchString(m.Revision) || len(m.Invocation) != 64 || m.EngineID == "" {
		return m, fmt.Errorf("build channel manifest unverified")
	}
	if _, err = hex.DecodeString(m.Invocation); err != nil {
		return m, fmt.Errorf("build invocation unverified")
	}
	return m, nil
}

// CaptureBuildResult runs an optional telemetry build with a private iidfile and confirms the final ID through inspect.
// The caller holds the image-tag lock throughout; build and inspect are the primary host process boundaries.
// Evidence failures leave an incomplete marker and never change the actual build result.
// An empty directory preserves the ordinary build's flags, queries, output and cache semantics.
func CaptureBuildResult(directory, tag, asset string, build func(string) error, inspect func(string) (string, error)) error {
	if directory == "" {
		return build("")
	}
	m, err := readBuildManifest(directory)
	if err != nil {
		return build("")
	}
	if !buildCommitLine.MatchString("COMMIT "+tag) || !strings.HasSuffix(tag, "-"+asset) || len(asset) != 64 {
		return build("")
	}
	pending, err := os.CreateTemp(directory, ".pending-")
	if err != nil {
		return build("")
	}
	marker := pending.Name()
	if err = pending.Close(); err != nil {
		return build("")
	}
	private, err := os.MkdirTemp(directory, "iid-")
	if err != nil {
		return build("")
	}
	defer os.RemoveAll(private)
	iid := filepath.Join(private, "result.id")
	if err = build(iid); err != nil {
		return err
	}
	data, err := cleanupRead(iid, 128)
	id := canonicalImageID(strings.TrimSpace(string(data)))
	if err != nil || !fullImageID(id) {
		return nil
	}
	confirmed, err := inspect(tag)
	if err != nil || canonicalImageID(confirmed) != id {
		return nil
	}
	event := buildPublication{Version: 1, Invocation: m.Invocation, Revision: m.Revision, EngineID: m.EngineID, Result: BuildResult{ID: id, Asset: asset, Step: "COMMIT " + tag, Source: "podman --iidfile + image inspect under tag lock"}}
	// Each producer owns a unique final filename; concurrent builds never overwrite each other's results.
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return nil
	}
	if writeJSON(filepath.Join(directory, "result-"+hex.EncodeToString(nonce)+".json"), event) == nil {
		_ = os.Remove(marker)
	}
	return nil
}

// readBuildResults admits only primary final results bound to the prepared invocation's channel.
func readBuildResults(o Options, before Snapshot) ([]BuildResult, error) {
	directory := filepath.Join(o.RunDir, "build-evidence")
	if _, err := os.Stat(filepath.Join(o.RunDir, "build-evidence-unverified")); !os.IsNotExist(err) {
		return nil, fmt.Errorf("build channel failed to initialize")
	}
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return nil, nil
	}
	m, err := readBuildManifest(directory)
	if err != nil || m.Revision != o.Revision || m.EngineID != before.EngineID {
		return nil, fmt.Errorf("build manifest changed")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) > 10001 {
		return nil, fmt.Errorf("build channel unreadable or oversized")
	}
	results := []BuildResult{}
	var total int64
	for _, entry := range entries {
		if entry.Name() == "manifest.json" {
			continue
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "result-") || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("build channel contains incomplete publication")
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() < 0 || info.Size() > 64*1024 {
			return nil, fmt.Errorf("build event unreadable or oversized")
		}
		total += info.Size()
		if total > cleanupPrivateLimit {
			return nil, fmt.Errorf("build channel exceeds total evidence limit")
		}
		var event buildPublication
		if entry.Type()&os.ModeSymlink != 0 || strictBuildJSON(filepath.Join(directory, entry.Name()), &event) != nil || event.Version != 1 || event.Invocation != m.Invocation || event.Revision != m.Revision || event.EngineID != m.EngineID || !fullImageID(event.Result.ID) || !strings.HasPrefix(event.Result.Step, "COMMIT ") || !cleanupProjectTag(strings.TrimPrefix(event.Result.Step, "COMMIT ")) || !strings.HasSuffix(event.Result.Step, "-"+event.Result.Asset) || len(event.Result.Asset) != 64 || event.Result.Source != "podman --iidfile + image inspect under tag lock" {
			return nil, fmt.Errorf("build result unverified")
		}
		results = append(results, event.Result)
	}
	return results, nil
}

// ConfirmBuildEngine binds a producer's primary engine-info query to the authorized private invocation.
func ConfirmBuildEngine(directory, engine string) error {
	manifest, err := readBuildManifest(directory)
	if err != nil || manifest.EngineID != engine {
		return fmt.Errorf("build engine differs from invocation")
	}
	return nil
}
