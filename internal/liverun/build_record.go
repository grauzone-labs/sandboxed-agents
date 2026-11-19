package liverun

import (
	"bufio"
	"bytes"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BuildResult carries a primary immutable result or a typed single-commit attempt from the trusted builder channel, never suite stdout.
// Source and Step identify the producer evidence; production final results use iidfile plus inspect, with policy and reproducibility facts.
type BuildResult struct {
	Policy          string
	Reproducibility string
	LayerInvocation string `json:",omitempty"` // Legacy v1 metadata is decoded but never grants ownership.
	Outcome         string
	ID              string
	Step            string
	Source          string
	Asset           string
	AttemptID       string
	PublishedAt     time.Time
	EndedAt         time.Time
}

// buildProof binds a full identity to its primary result or builder-controlled invocation evidence privately.
type buildProof struct {
	Token  string
	Step   string
	Result string
	FullID string
	Reason string
}

// buildLogPrefix admits only Go's source-location decoration, never an arbitrary substring.
var buildLogPrefix = regexp.MustCompile(`^[a-zA-Z0-9_./-]+\.go:[0-9]+: ?`)

// buildAssetLine recognizes the exact preflight version output rather than a substring.
var buildAssetLine = regexp.MustCompile(`^asset-sha256 ([0-9a-f]{64})$`)

// buildCommitLine binds primary final results to this project's canonical image-tag namespace.
var buildCommitLine = regexp.MustCompile(`^(?:\[[0-9]+/[0-9]+\] )?COMMIT localhost/sandboxed-agents:[a-z0-9-]+-[0-9a-f]{64}$`)

// parseBuildAsset reads only a comparison-context hint; stdout never supplies image ownership proof.
func parseBuildAsset(log []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	asset := ""
	conflict := false
	for scanner.Scan() {
		line := strings.TrimSpace(buildLogPrefix.ReplaceAllString(strings.TrimSpace(scanner.Text()), ""))
		if quoted, ok := strings.CutPrefix(line, "live output: "); ok {
			decoded, err := strconv.Unquote(quoted)
			if err != nil {
				continue
			}
			line = strings.TrimSpace(decoded)
		}
		if match := buildAssetLine.FindStringSubmatch(line); len(match) == 2 {
			if asset != "" && asset != match[1] {
				conflict = true
			}
			asset = match[1]
		}
	}
	if conflict {
		asset = ""
	}
	return asset, scanner.Err()
}

// resolveBuildRecord uniquely joins explicit build results to full, inspected engine identities.
func resolveBuildRecord(proofs []buildProof, images []cleanupImage) []buildProof {
	byID := map[string]cleanupImage{}
	for _, image := range images {
		byID[image.ID] = image
	}
	for i := range proofs {
		proof := &proofs[i]
		if proof.Reason != "" {
			continue
		}
		id := canonicalImageID(proof.Token)
		image, present := byID[id]
		switch {
		case !fullImageID(id):
			proof.Reason = "primary result lacks a complete immutable image ID"
		case !present:
			proof.Reason = "build result absent from complete current inventory"
		case !image.Verified:
			proof.Reason = "image inspection failed; build result unverified"
		default:
			proof.FullID = id
		}
	}
	return proofs
}

// cleanupPlan requires every eligibility fact independently and orders children before proven parents.
func cleanupPlan(before Snapshot, images []cleanupImage, proofs []buildProof, asset string, retain []string) []CleanupItem {
	present := map[string]bool{}
	proven := map[string]bool{}
	retained := map[string]bool{}
	for _, image := range before.Images {
		present[canonicalImageID(image.ID)] = true
	}
	for _, proof := range proofs {
		if proof.FullID != "" {
			proven[proof.FullID] = true
		}

	}
	for _, id := range retain {
		retained[canonicalImageID(id)] = true
	}
	byID := map[string]cleanupImage{}
	items := map[string]CleanupItem{}
	for _, image := range images {
		byID[image.ID] = image
		item := CleanupItem{ID: image.ID, Tags: cleanupTags(image.RepoTags), SizeBytes: image.Size, SizeVerified: image.Verified}
		switch {
		case retained[image.ID]:
			item.Reason = "retained by user"
		case present[image.ID]:
			item.Reason = "present before invocation"
		case !image.Verified:
			item.Reason = "image inspection failed; unverified"
			item.Query = "podman image inspect ID"
		case foreignImageTags(image.RepoTags):
			item.Reason = "foreign or mixed image tags"
		case !proven[image.ID]:
			item.Reason = "missing exact build proof"
		case cleanupWrongHash(image.Labels, asset):
			item.Reason = "asset hash differs from invocation"
		}
		items[image.ID] = item
	}
	// Topological order uses only inspect-confirmed identities. It grants no ownership.
	children := map[string][]string{}
	for _, image := range images {
		for _, parent := range []string{image.Parent, image.Labels["io.sandboxed-agents.base-image"]} {
			parent = canonicalImageID(parent)
			if image.Verified && fullImageID(parent) && byID[parent].Verified && parent != image.ID {
				children[parent] = append(children[parent], image.ID)
			}
		}
	}
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := []CleanupItem{}
	state := map[string]int{}
	cyclic := false
	var visit func(string)
	visit = func(id string) {
		if state[id] == 1 {
			cyclic = true
			return
		}
		if state[id] == 2 {
			return
		}
		state[id] = 1
		sort.Strings(children[id])
		for _, child := range children[id] {
			visit(child)
		}
		state[id] = 2
		result = append(result, items[id])
	}
	for _, id := range ids {
		visit(id)
	}
	if cyclic {
		for i := range result {
			if result[i].Reason == "" {
				result[i].Reason = "image dependency cycle; unverified"
			}
		}
	}
	return result
}

// cleanupAssetLabel is the product label checked independently of build-result provenance.
const cleanupAssetLabel = "io.sandboxed-agents.asset-sha256"

// cleanupWrongHash treats any present asset label, including an empty one, as a required match.
func cleanupWrongHash(labels map[string]string, asset string) bool {
	value, labelled := labels[cleanupAssetLabel]
	return labelled && (asset == "" || value != asset)
}

// foreignImageTags refuses every tag outside the canonical project namespace, allowing untagged intermediates.
func foreignImageTags(tags []string) bool {
	for _, tag := range tags {
		if tag != "<none>:<none>" && !cleanupProjectTag(tag) {
			return true
		}
	}
	return false
}

// uniqueBuildRecord retains one independently verified result per full ID without assigning it to unrelated attempts.
func uniqueBuildRecord(records []buildProof) []buildProof {
	result := []buildProof{}
	seen := map[string]bool{}
	for _, record := range records {
		key := record.FullID
		if key == "" {
			key = record.Token + "\x00" + record.Reason
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, record)
		}
	}
	return result
}

// accountBuildCoverage classifies retained unknowns without granting any image deletion authority.
func accountBuildCoverage(run *cleanupRun, plan []CleanupItem, record []buildProof, images []cleanupImage, state buildChannelState) {
	external := map[string]bool{}
	for _, image := range images {
		_, assetPresent := image.Labels[cleanupAssetLabel]
		_, productPresent := image.Labels["io.sandboxed-agents.image"]
		external[image.ID] = state.Inputs[image.ID] && image.Verified && !assetPresent && !productPresent
	}
	before := map[string]bool{}
	receipted := map[string]bool{}
	for _, image := range run.before.Images {
		before[canonicalImageID(image.ID)] = true
	}
	for _, proof := range record {
		if fullImageID(proof.Token) {
			receipted[canonicalImageID(proof.Token)] = true
		}
	}
	for i := range plan {
		item := &plan[i]
		if before[item.ID] || receipted[item.ID] || !item.SizeVerified {
			continue
		}
		if state.Complete && !state.Open || external[item.ID] {
			if item.Reason == "missing exact build proof" || item.Reason == "foreign or mixed image tags" {
				item.Reason = "not committed by this run's builds"
			}
		} else if (state.Open || !state.Complete) && item.Reason == "foreign or mixed image tags" {
			run.report.Status = "partial"
		}
	}
}
