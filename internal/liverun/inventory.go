package liverun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// inventory reads all images and containers and bulk-inspects their exact references.
func (s *Service) inventory(ctx context.Context, o Options, before *Snapshot) error {
	data, err := s.podman(ctx, o, before.Target, "images", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return err
	}
	var images []struct{ ID string }
	if err = json.Unmarshal(data, &images); err != nil || images == nil {
		return fmt.Errorf("image inventory is not a complete array: %v", err)
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, image := range images {
		if !fullImageID(image.ID) {
			return fmt.Errorf("abbreviated image ID")
		}
		image.ID = canonicalImageID(image.ID)
		if !seen[image.ID] {
			ids = append(ids, image.ID)
			seen[image.ID] = true
		}
	}
	if len(ids) > 0 {
		data, err = s.podman(ctx, o, before.Target, append([]string{"image", "inspect"}, ids...)...)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &before.Images); err != nil {
			return err
		}
		if len(before.Images) != len(ids) {
			return fmt.Errorf("incomplete image inspection")
		}
		for i := range before.Images {
			image := &before.Images[i]
			image.ID = canonicalImageID(image.ID)
			if !seen[image.ID] || image.Created.IsZero() {
				return fmt.Errorf("invalid image inspection")
			}
			delete(seen, image.ID)
		}
		if len(seen) != 0 {
			return fmt.Errorf("missing image inspection")
		}
	}
	data, err = s.podman(ctx, o, before.Target, "ps", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return err
	}
	var containers []struct{ ID string }
	if err = json.Unmarshal(data, &containers); err != nil || containers == nil {
		return fmt.Errorf("container inventory is not a complete array: %v", err)
	}
	ids = nil
	for _, container := range containers {
		if !validName(container.ID) {
			return fmt.Errorf("invalid container identity")
		}
		ids = append(ids, container.ID)
	}
	if len(ids) > 0 {
		data, err = s.podman(ctx, o, before.Target, append([]string{"container", "inspect"}, ids...)...)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &before.Containers); err != nil {
			return err
		}
		if len(before.Containers) != len(ids) {
			return fmt.Errorf("incomplete container inspection")
		}
		for i := range before.Containers {
			container := &before.Containers[i]
			container.Image = canonicalImageID(container.Image)
			if !fullImageID(container.Image) {
				return fmt.Errorf("invalid container image reference")
			}
		}
	}
	for _, container := range before.Containers {
		if container.State.Status == "running" {
			before.HostLoad = "shared"
		}
	}
	return nil
}

// fullImageID admits exact full hexadecimal image identities only.
func fullImageID(id string) bool {
	return regexp.MustCompile("^(sha256:)?[0-9a-f]{64}$").MatchString(id)
}

// hostEnvironment removes ambient Windows target overrides and sets suite inputs explicitly.
func hostEnvironment(platform string, values map[string]string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if platform == "windows" && (upper == "CONTAINER_HOST" || upper == "CONTAINER_CONNECTION" || upper == "CONTAINER_SSHKEY") {
			continue
		}
		if _, ok := values[upper]; ok {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// canonicalImageID normalizes Podman's full hexadecimal image identities across list and inspect.
func canonicalImageID(id string) string {
	if len(id) == 64 {
		return "sha256:" + id
	}
	return id
}
