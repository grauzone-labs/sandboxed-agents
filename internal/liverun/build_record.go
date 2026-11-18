package liverun

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// buildProof preserves an exact result and its immediately preceding step privately.
type buildProof struct {
	Token  string
	Step   string
	Result string
	Line   int
	FullID string
	Reason string
}

// buildLogPrefix admits only Go's source-location decoration, never an arbitrary substring.
var buildLogPrefix = regexp.MustCompile(`^[a-zA-Z0-9_./-]+\.go:[0-9]+: ?`)

// buildAssetLine recognizes the exact preflight version output rather than a substring.
var buildAssetLine = regexp.MustCompile(`^asset-sha256 ([0-9a-f]{64})$`)

// buildStepLine recognizes non-FROM build steps with Podman's optional stage prefix.
var buildStepLine = regexp.MustCompile(`^(?:\[[0-9]+/[0-9]+\] )?STEP [0-9]+/[0-9]+: (RUN|COPY|ADD|LABEL|ENV|USER|WORKDIR|EXPOSE|VOLUME|ENTRYPOINT|CMD|ARG|SHELL|STOPSIGNAL|HEALTHCHECK|ONBUILD|MAINTAINER) .+$`)

// buildResultLine admits only an exact Podman result ID, never a layer-download description.
var buildResultLine = regexp.MustCompile(`^--> ((?:sha256:)?[0-9a-f]{12,64})$`)

// buildCommitLine binds final results to this project's canonical image-tag namespace.
var buildCommitLine = regexp.MustCompile(`^(?:\[[0-9]+/[0-9]+\] )?COMMIT localhost/sandboxed-agents:[a-z0-9-]+-[0-9a-f]{64}$`)

// parseBuildOutput extracts adjacent step/result pairs and the exact preflight asset-hash line.
// Nonadjacent, cached, interleaved, FROM, bare-ID and download output deliberately proves nothing.
func parseBuildOutput(log []byte) ([]buildProof, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	previous := ""
	lineNumber := 0
	asset := ""
	assetConflict := false
	proofs := []buildProof{}
	seen := map[string]bool{}
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		line = buildLogPrefix.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if match := buildAssetLine.FindStringSubmatch(line); len(match) == 2 {
			if asset != "" && asset != match[1] {
				assetConflict = true
			}
			asset = match[1]
		}
		if match := buildResultLine.FindStringSubmatch(line); len(match) == 2 && (buildStepLine.MatchString(previous) || buildCommitLine.MatchString(previous)) {
			key := previous + "\n" + line
			if !seen[key] {
				proofs = append(proofs, buildProof{Token: match[1], Step: previous, Result: line, Line: lineNumber})
				seen[key] = true
			}
			if len(proofs) > 10000 {
				return nil, "", fmt.Errorf("too many build results")
			}
		}
		previous = line
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	if assetConflict {
		asset = ""
	}
	return proofs, asset, nil
}

// resolveBuildRecord uniquely joins explicit build results to full, inspected engine identities.
func resolveBuildRecord(proofs []buildProof, images []cleanupImage) []buildProof {
	for i := range proofs {
		proof := &proofs[i]
		prefix := strings.TrimPrefix(proof.Token, "sha256:")
		matches := []cleanupImage{}
		for _, image := range images {
			if strings.HasPrefix(strings.TrimPrefix(image.ID, "sha256:"), prefix) {
				matches = append(matches, image)
			}
		}
		switch {
		case len(matches) == 0:
			proof.Reason = "build result absent from complete current inventory"
		case len(matches) > 1:
			proof.Reason = "ambiguous build ID; multiple full inventory matches"
		case !matches[0].Verified:
			proof.Reason = "image inspection failed; build result unverified"
		default:
			proof.FullID = matches[0].ID
		}
	}
	return proofs
}

// cleanupPlan requires every eligibility fact independently and orders children before proven parents.
func cleanupPlan(before Snapshot, images []cleanupImage, proofs []buildProof, asset string, retain []string) []CleanupItem {
	present := map[string]bool{}
	proven := map[string]bool{}
	ambiguous := map[string]bool{}
	retained := map[string]bool{}
	for _, image := range before.Images {
		present[canonicalImageID(image.ID)] = true
	}
	for _, proof := range proofs {
		if proof.FullID != "" {
			proven[proof.FullID] = true
		}
		if strings.HasPrefix(proof.Reason, "ambiguous") {
			for _, image := range images {
				if strings.HasPrefix(strings.TrimPrefix(image.ID, "sha256:"), strings.TrimPrefix(proof.Token, "sha256:")) {
					ambiguous[image.ID] = true
				}
			}
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
		case ambiguous[image.ID]:
			item.Reason = "ambiguous build ID"
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
