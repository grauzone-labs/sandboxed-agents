package liverun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cleanupImage augments the shared image snapshot with inspect-only removal metadata.
type cleanupImage struct {
	Image
	Parent   string
	Size     uint64
	Verified bool
}

// cleanupQuery captures bounded private answers and returns only the failing command category.
func (s *Service) cleanupQuery(ctx context.Context, r *cleanupRun, target Target, args ...string) ([]byte, error) {
	query := append([]string(nil), args...)
	if target.Connection != "" {
		query = append([]string{"--connection", target.Connection}, query...)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := s.Executor.Execute(bounded, Invocation{Program: "podman", Args: query, Dir: r.options.Checkout, Env: hostEnvironment(s.Platform, nil)})
	r.queries++
	evidence := struct {
		Args   []string
		Output string
		Error  string
	}{Args: query}
	if err != nil {
		evidence.Error = boundedCleanupText(err.Error(), 4096)
	}
	if len(data) > cleanupPrivateLimit {
		evidence.Error = "query output exceeds limit"
		err = fmt.Errorf("query output exceeds limit")
	}
	// Retain as much output as the encoded envelope allows, so evidence size never refuses an answer the raw limit admits.
	envelope, _ := json.Marshal(evidence)
	evidence.Output = cleanupEvidenceText(data, cleanupPrivateLimit/2-len(envelope))
	if saveErr := cleanupWriteJSON(filepath.Join(r.directory, fmt.Sprintf("query-%05d.json", r.queries)), evidence, cleanupPrivateLimit); saveErr != nil {
		if err == nil {
			return nil, errCleanupEvidenceLost
		}
		return nil, fmt.Errorf("private query evidence could not be retained")
	}
	if err != nil {
		return nil, engineFault("engine unreachable", engineReason(fmt.Errorf("%w; %s", err, data)), err)
	}
	return data, nil
}

// errCleanupEvidenceLost marks a command that succeeded although its private query evidence could not be retained.
// Read-only callers still treat it as a failed query; a removal caller keeps the removal for final reconciliation.
var errCleanupEvidenceLost = errors.New("private query evidence could not be retained")

// errCleanupUnreachable keeps the engine-unreachable pending form distinct from an invalid identity.
var errCleanupUnreachable = fmt.Errorf("engine connection unavailable")

// cleanupEngine checks the same selected endpoint before any destructive operation.
func (s *Service) cleanupEngine(ctx context.Context, r *cleanupRun, target Target) error {
	data, err := s.cleanupQuery(ctx, r, target, "info", "--format", "json")
	if err != nil {
		return cleanupPendingEngine(err)
	}
	identity, _, err := EngineIdentity(data)
	if err != nil || identity != r.before.EngineID {
		return fmt.Errorf("engine identity changed or unverified")
	}
	return nil
}

// cleanupInventory batches exact primary image inspections and falls back per ID on incomplete or failed answers.
func (s *Service) cleanupInventory(ctx context.Context, r *cleanupRun, target Target) ([]cleanupImage, []CleanupItem, error) {
	if err := s.cleanupEngine(ctx, r, target); err != nil {
		return nil, []CleanupItem{{Reason: "engine identity unverified", Query: "podman info --format json"}}, err
	}
	data, err := s.cleanupQuery(ctx, r, target, "images", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, []CleanupItem{{Reason: "image inventory unverified", Query: "podman images --all --no-trunc"}}, err
	}
	var list []struct{ ID string }
	if json.Unmarshal(data, &list) != nil || list == nil || len(list) > 10000 {
		return nil, []CleanupItem{{Reason: "incomplete or oversized image inventory", Query: "podman images --all --no-trunc"}}, fmt.Errorf("image inventory unverified")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, entry := range list {
		id := canonicalImageID(entry.ID)
		if !fullImageID(id) {
			return nil, []CleanupItem{{Reason: "abbreviated image inventory", Query: "podman images --all --no-trunc"}}, fmt.Errorf("image inventory unverified")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	images := []cleanupImage{}
	issues := []CleanupItem{}
	for start := 0; start < len(ids); start += inspectBatchSize {
		batch := ids[start:min(start+inspectBatchSize, len(ids))]
		inspected, queryErr := s.cleanupQuery(ctx, r, target, append([]string{"image", "inspect"}, batch...)...)
		decoded, valid := decodeCleanupImages(inspected, batch)
		if queryErr != nil || !valid {
			decoded = map[string]cleanupImage{}
			for _, id := range batch {
				answer, singleErr := s.cleanupQuery(ctx, r, target, "image", "inspect", id)
				single, ok := decodeCleanupImages(answer, []string{id})
				if singleErr == nil && ok {
					decoded[id] = single[id]
				}
			}
		}
		for _, id := range batch {
			image, verified := decoded[id]
			if !verified {
				image = cleanupImage{Image: Image{ID: id}}
				issues = append(issues, CleanupItem{ID: id, Reason: "image inspection failed or incomplete", Query: "podman image inspect ID"})
			}
			images = append(images, image)
		}
	}
	if _, err = s.cleanupReferences(ctx, r, target); err != nil {
		issues = append(issues, CleanupItem{Reason: "container references unverified", Query: "podman ps --all / container inspect"})
		return images, issues, err
	}
	if err = cleanupWriteJSON(filepath.Join(r.directory, fmt.Sprintf("inventory-%05d.json", r.queries)), images, cleanupPrivateLimit); err != nil {
		return nil, issues, fmt.Errorf("private inventory could not be retained")
	}
	return images, issues, nil
}

// cleanupReferences verifies exact image references for every container, independent of controller group.
func (s *Service) cleanupReferences(ctx context.Context, r *cleanupRun, target Target) (map[string]bool, error) {
	data, err := s.cleanupQuery(ctx, r, target, "ps", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []struct{ ID string }
	if json.Unmarshal(data, &list) != nil || list == nil || len(list) > 10000 {
		return nil, fmt.Errorf("container inventory incomplete")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, entry := range list {
		if !validName(entry.ID) || seen[entry.ID] {
			return nil, fmt.Errorf("container inventory invalid")
		}
		seen[entry.ID] = true
		ids = append(ids, entry.ID)
	}
	used := map[string]bool{}
	if len(ids) == 0 {
		r.containers = []Container{}
		return used, nil
	}
	containers, err := inspectBatches[Container](ids, func(batch []string) ([]byte, error) {
		return s.cleanupQuery(ctx, r, target, append([]string{"container", "inspect"}, batch...)...)
	})
	if err != nil {
		return nil, err
	}
	if len(containers) != len(ids) {
		return nil, fmt.Errorf("container inspection incomplete")
	}
	for _, container := range containers {
		if !seen[container.ID] || !fullImageID(container.Image) {
			return nil, fmt.Errorf("container inspection invalid")
		}
		delete(seen, container.ID)
		used[canonicalImageID(container.Image)] = true
	}
	if len(seen) != 0 {
		return nil, fmt.Errorf("container inspection incomplete")
	}
	r.containers = containers
	return used, nil
}

// cleanupTags excludes host-specific image names while retaining this project's canonical tags.
func cleanupTags(tags []string) []string {
	result := []string{}
	for _, tag := range tags {
		if cleanupProjectTag(tag) || tag == "<none>:<none>" {
			result = append(result, tag)
		} else {
			result = append(result, "<redacted>")
		}
		if len(result) == 32 {
			break
		}
	}
	return result
}

// boundedCleanupText caps private evidence strings without interpreting external output.
func boundedCleanupText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "") + " [truncated]"
}

// cleanupEvidenceText returns the longest retained prefix of value, halving it on overflow, whose JSON encoding,
// escaping and truncation marker included, fits within budget bytes. The first prefix is at most budget bytes, and
// only one byte beyond the prefix is copied to keep the truncation marker, so an oversized answer is never copied or
// encoded in full.
func cleanupEvidenceText(value []byte, budget int) string {
	for limit := min(len(value), max(budget, 0)); ; limit /= 2 {
		text := boundedCleanupText(string(value[:min(len(value), limit+1)]), limit)
		encoded, err := json.Marshal(text)
		if err == nil && len(encoded) <= budget || limit == 0 {
			return text
		}
	}
}

// cleanupProjectTag exports only known toolchain names from a project image tag.
func cleanupProjectTag(tag string) bool {
	if !buildCommitLine.MatchString("COMMIT " + tag) {
		return false
	}
	value := strings.TrimPrefix(tag, "localhost/sandboxed-agents:")
	set := value[:len(value)-65]
	if set == "none" {
		return true
	}
	previous := ""
	for _, name := range strings.Split(set, "-") {
		if previous != "" && name <= previous {
			return false
		}
		previous = name
		switch name {
		case "azure", "dotnet", "native", "playwright":
		default:
			return false
		}
	}
	return true
}

// decodeCleanupImages validates the complete exact-ID answer of one bounded primary inspect batch.
func decodeCleanupImages(data []byte, ids []string) (map[string]cleanupImage, bool) {
	var answer []cleanupImage
	var fields []map[string]json.RawMessage
	if json.Unmarshal(data, &answer) != nil || json.Unmarshal(data, &fields) != nil || len(answer) != len(ids) || len(fields) != len(ids) {
		return nil, false
	}
	expected := map[string]bool{}
	for _, id := range ids {
		expected[id] = true
	}
	result := map[string]cleanupImage{}
	for i, image := range answer {
		image.ID = canonicalImageID(image.ID)
		image.Parent = canonicalImageID(image.Parent)
		if !expected[image.ID] || result[image.ID].Verified || fields[i]["Size"] == nil || string(fields[i]["Size"]) == "null" || image.Created.IsZero() || image.Parent != "" && !fullImageID(image.Parent) {
			return nil, false
		}
		image.Verified = true
		result[image.ID] = image
	}
	return result, len(result) == len(ids)
}
