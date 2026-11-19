package liverun

import (
	"context"
	"encoding/json"
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
	}{Args: query, Output: boundedCleanupText(string(data), cleanupPrivateLimit/2)}
	if err != nil {
		evidence.Error = boundedCleanupText(err.Error(), 4096)
	}
	if len(data) > cleanupPrivateLimit {
		evidence.Error = "query output exceeds limit"
		err = fmt.Errorf("query output exceeds limit")
	}
	if saveErr := cleanupWriteJSON(filepath.Join(r.directory, fmt.Sprintf("query-%05d.json", r.queries)), evidence, cleanupPrivateLimit); saveErr != nil {
		return nil, fmt.Errorf("private query evidence could not be retained")
	}
	if err != nil {
		return nil, engineFault("engine unreachable", engineReason(fmt.Errorf("%w; %s", err, data)), err)
	}
	return data, nil
}

// errCleanupUnreachable keeps the engine-unreachable pending form distinct from an invalid identity.
var errCleanupUnreachable = fmt.Errorf("engine connection unavailable")

// cleanupEngine checks the same selected endpoint before any destructive operation.
func (s *Service) cleanupEngine(ctx context.Context, r *cleanupRun, target Target) error {
	data, err := s.cleanupQuery(ctx, r, target, "info", "--format", "json")
	if err != nil {
		return cleanupPendingEngine(err)
	}
	var info struct {
		Host  struct{ Hostname string }
		Store struct{ GraphRoot string }
	}
	if json.Unmarshal(data, &info) != nil || info.Host.Hostname+":"+info.Store.GraphRoot != r.before.EngineID {
		return fmt.Errorf("engine identity changed or unverified")
	}
	return nil
}

// cleanupInventory enumerates full IDs and individually inspects each image, preserving query failures.
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
	for _, id := range ids {
		image := cleanupImage{Image: Image{ID: id}}
		inspected, e := s.cleanupQuery(ctx, r, target, "image", "inspect", id)
		var answer []cleanupImage
		var fields []map[string]json.RawMessage
		_ = json.Unmarshal(inspected, &fields)
		if e == nil && json.Unmarshal(inspected, &answer) == nil && len(answer) == 1 && len(fields) == 1 && fields[0]["Size"] != nil && string(fields[0]["Size"]) != "null" && canonicalImageID(answer[0].ID) == id && !answer[0].Created.IsZero() {
			image = answer[0]
			image.ID = id
			image.Parent = canonicalImageID(image.Parent)
			image.Verified = true
			if image.Parent != "" && !fullImageID(image.Parent) {
				image.Verified = false
			}
		}
		if !image.Verified {
			issues = append(issues, CleanupItem{ID: id, Reason: "image inspection failed or incomplete", Query: "podman image inspect ID"})
		}
		images = append(images, image)
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
	data, err = s.cleanupQuery(ctx, r, target, append([]string{"container", "inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var containers []Container
	if json.Unmarshal(data, &containers) != nil || len(containers) != len(ids) {
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
