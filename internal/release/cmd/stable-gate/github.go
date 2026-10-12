package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

// All GitHub operations in this adapter are reads. Publication is owned by the
// workflow caller, which must depend on a successful gate.
func collectEvidence(repository, tag, commit, hash string) (release.StableGateEvidence, error) {
	evidence := release.StableGateEvidence{Tag: tag, Commit: commit, AssetHash: hash}
	version, err := release.StableTagVersion(tag)
	if err != nil {
		return evidence, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return evidence, fmt.Errorf("usage: --repository OWNER/REPO and --commit full SHA are required")
	}
	if runtime.GOOS != "linux" {
		return evidence, fmt.Errorf("prerelease: GitHub adapter requires the Linux release runner")
	}
	base := "repos/" + repository
	var releases []githubRelease
	if err := githubPages(base+"/releases?per_page=100", func(raw json.RawMessage) error {
		var page []githubRelease
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		releases = append(releases, page...)
		return nil
	}); err != nil {
		return evidence, fmt.Errorf("prerelease: %w", err)
	}
	var refusal error
	var versionRefusal error
	var lookupRefusal error
	ciCollected := false
	for _, preview := range releases {
		previewVersion := release.PreviewTagVersion(preview.TagName)
		if !preview.Prerelease || preview.Draft || previewVersion == "" {
			continue
		}
		var resolved struct {
			SHA string `json:"sha"`
		}
		if err := githubJSON(base+"/commits/"+preview.TagName, &resolved); err != nil {
			if lookupRefusal == nil {
				lookupRefusal = fmt.Errorf("prerelease tag %s: %w", preview.TagName, err)
			}
			continue
		}
		if resolved.SHA != commit {
			continue
		}
		if previewVersion != version {
			versionRefusal = fmt.Errorf("prerelease version: no preview for stable tag %s on commit %s", tag, commit)
			continue
		}
		if !ciCollected {
			if err := collectCI(base, &evidence); err != nil {
				if lookupRefusal != nil {
					return evidence, fmt.Errorf("%v; %w", lookupRefusal, err)
				}
				return evidence, err
			}
			ciCollected = true
		}
		candidate, err := collectPreviewRecords(base, preview, resolved.SHA)
		if err == nil {
			evidence.Preview = candidate
			err = release.ValidateStableGatePrerequisites(evidence)
		}
		if err == nil {
			candidate.AssetHash, err = collectPreviewHash(base, preview)
		}
		if err == nil {
			err = release.ValidateStableAssetHash(evidence.AssetHash, candidate.AssetHash)
		}
		if err == nil {
			return evidence, nil
		}
		if refusal == nil {
			refusal = err
		}
	}
	// Preserve a named reason when candidates exist but none validates. A
	// missing preview stays nil so the evaluator gives its prerelease refusal.
	for _, additional := range []error{versionRefusal, lookupRefusal} {
		if additional == nil {
			continue
		}
		if refusal == nil {
			refusal = additional
		} else {
			refusal = fmt.Errorf("%w; %v", refusal, additional)
		}
	}
	if refusal == nil {
		return evidence, release.EvaluateStableGate(evidence)
	}

	return evidence, refusal
}

func collectPreviewRecords(base string, preview githubRelease, commit string) (*release.PreviewEvidence, error) {
	candidate := &release.PreviewEvidence{Tag: preview.TagName, Commit: commit, Records: map[string][]byte{}}
	for _, filename := range release.ValidationRecordFilenames() {
		assetID := preview.assetID(filename)
		if assetID == 0 {
			return nil, fmt.Errorf("%s: validation file missing from prerelease", filename)
		}
		data, err := githubAsset(base, assetID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filename, err)
		}
		candidate.Records[filename] = data
	}
	return candidate, nil
}

func collectPreviewHash(base string, preview githubRelease) (string, error) {
	id := preview.assetID("sandboxed-agents-linux-amd64")
	if id == 0 {
		return "", fmt.Errorf("asset hash: Linux preview binary missing")
	}
	data, err := githubAsset(base, id)
	if err != nil {
		return "", fmt.Errorf("asset hash: %w", err)
	}
	return previewAssetHash(data)
}

func collectCI(base string, evidence *release.StableGateEvidence) error {
	// Runs are newest first. An old passing run never hides a newer failed or
	// still-running push run on the same commit.
	var latest *githubRun
	if err := githubPages(base+"/actions/workflows/release-reproducibility.yml/runs?head_sha="+evidence.Commit+"&event=push&per_page=100", func(raw json.RawMessage) error {
		var page struct {
			Runs []githubRun `json:"workflow_runs"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, run := range page.Runs {
			if latest == nil && run.HeadSHA == evidence.Commit {
				copy := run
				latest = &copy
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("CI: %w", err)
	}
	if latest == nil {
		return nil
	}
	evidence.CICommit = latest.HeadSHA
	if latest.Status != "completed" || latest.Conclusion != "success" {
		return nil
	}
	passed := map[string]bool{}
	if err := githubPages(fmt.Sprintf("%s/actions/runs/%d/jobs?per_page=100", base, latest.ID), func(raw json.RawMessage) error {
		var page struct {
			Jobs []struct{ Name, Status, Conclusion string } `json:"jobs"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, job := range page.Jobs {
			passed[job.Name] = job.Status == "completed" && job.Conclusion == "success"
		}
		return nil
	}); err != nil {
		return fmt.Errorf("CI: %w", err)
	}
	evidence.CIGreen = passed["linux"] && passed["windows"]
	return nil
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
	Assets     []struct {
		ID   int64
		Name string
	} `json:"assets"`
}

func (r githubRelease) assetID(name string) int64 {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset.ID
		}
	}
	return 0
}

type githubRun struct {
	ID                 int64
	HeadSHA            string `json:"head_sha"`
	Status, Conclusion string
}

func githubAsset(base string, id int64) ([]byte, error) {
	return github(fmt.Sprintf("%s/releases/assets/%d", base, id), "-H", "Accept: application/octet-stream")
}
func githubJSON(endpoint string, target any) error {
	data, err := github(endpoint)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	return nil
}
func githubPages(endpoint string, page func(json.RawMessage) error) error {
	data, err := github(endpoint, "--paginate")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid GitHub response")
		}
		if err := page(raw); err != nil {
			return fmt.Errorf("invalid GitHub response")
		}
	}
}
func github(endpoint string, args ...string) ([]byte, error) {
	command := exec.Command("gh", append([]string{"api", endpoint}, args...)...)
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("GitHub evidence lookup failed for %s", endpoint)
	}
	return data, nil
}
func previewAssetHash(data []byte) (string, error) {
	directory, err := os.MkdirTemp("", "stable-gate-preview-")
	if err != nil {
		return "", fmt.Errorf("asset hash: cannot create preview directory")
	}
	defer os.RemoveAll(directory)
	binary := filepath.Join(directory, "sandboxed-agents")
	if err := os.WriteFile(binary, data, 0700); err != nil {
		return "", fmt.Errorf("asset hash: cannot save preview binary")
	}
	output, err := exec.Command(binary, "version").Output()
	if err != nil {
		return "", fmt.Errorf("asset hash: preview version command failed")
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if regexp.MustCompile(`^asset-sha256 [0-9a-f]{64}$`).MatchString(line) {
			return strings.TrimPrefix(line, "asset-sha256 "), nil
		}
	}
	return "", fmt.Errorf("asset hash: preview version did not report a hash")
}
