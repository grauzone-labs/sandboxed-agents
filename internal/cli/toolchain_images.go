package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

const (
	// ToolchainsLabel records the canonical toolchain set on sandboxes and images.
	ToolchainsLabel  = "io.sandboxed-agents.toolchains"
	imageMarkerLabel = "io.sandboxed-agents.image"
	assetHashLabel   = "io.sandboxed-agents.asset-sha256"
	baseImageLabel   = "io.sandboxed-agents.base-image"
)

func toolchainSet(set []string) string {
	if len(set) == 0 {
		return "none"
	}
	return strings.Join(set, ",")
}

func sandboxToolchains(container sandboxContainer) ([]string, error) {
	value := container.Config.Labels[ToolchainsLabel]
	if value == "" {
		value = imageToolchains(container.Config.Image)
		if value == "-" {
			return nil, fmt.Errorf("recorded toolchain set is unavailable from image %q", container.Config.Image)
		}
		value = strings.ReplaceAll(value, "-", ",")
	}
	return recordedToolchains(value)
}

func recordedToolchains(value string) ([]string, error) {
	if value == "none" {
		return nil, nil
	}
	set := strings.Split(value, ",")
	for _, name := range set {
		if !slices.Contains(catalog.Default.Toolchains, name) {
			return nil, fmt.Errorf("invalid recorded toolchain set %q", value)
		}
	}
	slices.Sort(set)
	set = slices.Compact(set)
	return set, nil
}

type imageRecord struct {
	ID     string `json:"Id"`
	Labels map[string]string
}

func inspectImage(p podmanTarget, name string) (imageRecord, error) {
	output, err := p.query("image", "inspect", name)
	if err != nil {
		return imageRecord{}, err
	}
	var records []imageRecord
	if err := json.Unmarshal([]byte(output), &records); err != nil || len(records) != 1 || records[0].ID == "" {
		return imageRecord{}, fmt.Errorf("read image %s: invalid inspect answer", name)
	}
	return records[0], nil
}

func buildLabeledImage(p podmanTarget, set []string, hash, baseID string, refresh bool, output io.Writer) (string, error) {
	return image.BuildWithToolchains(context.Background(), nil, set, func(ctx context.Context, args ...string) error {
		flags := []string{"--label", imageMarkerLabel + "=sandboxed-agents", "--label", assetHashLabel + "=" + hash, "--label", ToolchainsLabel + "=" + toolchainSet(set)}
		if refresh {
			flags = append(flags, "--no-cache")
		}
		if len(set) == 0 {
			if refresh {
				flags = append(flags, "--pull=always")
			}
		} else {
			flags = append(flags, "--pull=never", "--build-arg", "BASE_IMAGE="+baseID, "--label", baseImageLabel+"="+baseID)
		}
		args = append(append(append([]string{}, args[:len(args)-1]...), flags...), args[len(args)-1])
		result, err := p.query(args...)
		if result != "" {
			fmt.Fprint(output, result)
		}
		return err
	})
}

// rebuildImages discovers images from labels before rebuilding, preserving tags on failed builds.
func rebuildImages(request Request, p podmanTarget, output io.Writer) error {
	hash, err := image.Hash(image.Context())
	if err != nil {
		return err
	}
	inventory, err := p.query("image", "ls", "--filter", "label="+imageMarkerLabel+"=sandboxed-agents", "--filter", "label="+assetHashLabel+"="+hash, "--format", "json")
	if err != nil {
		return err
	}
	var records []imageRecord
	if err := json.Unmarshal([]byte(inventory), &records); err != nil || records == nil {
		return fmt.Errorf("read image inventory: invalid answer")
	}
	sets := map[string][]string{}
	if len(request.Toolchains) > 0 {
		sets[toolchainSet(request.Toolchains)] = request.Toolchains
	}
	for _, record := range records {
		if record.ID == "" {
			return fmt.Errorf("read image inventory: missing image ID")
		}
		current, err := inspectImage(p, record.ID)
		if err != nil {
			return err
		}
		set, err := recordedToolchains(current.Labels[ToolchainsLabel])
		if err != nil {
			return err
		}
		if len(set) > 0 {
			sets[toolchainSet(set)] = set
		}
	}
	baseTag, err := buildLabeledImage(p, nil, hash, "", true, output)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "built image", baseTag)
	// Once the base changes, all pre-existing sandboxes retain their old images.
	const notice = "existing sandboxes keep their image until update; list marks them as outdated"
	if len(sets) == 0 {
		fmt.Fprintln(output, notice)
		return nil
	}
	base, err := inspectImage(p, baseTag)
	if err != nil {
		return fmt.Errorf("%w; %s", err, notice)
	}
	var names []string
	for name := range sets {
		names = append(names, name)
	}
	slices.Sort(names)
	var failures []error
	var failedSets []string
	for _, name := range names {
		tag, err := buildLabeledImage(p, sets[name], hash, base.ID, true, output)
		if err != nil {
			failures = append(failures, err)
			failedSets = append(failedSets, name)
			continue
		}
		fmt.Fprintln(output, "built image", tag)
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed toolchain sets: %s; %s: %w", strings.Join(failedSets, "; "), notice, errors.Join(failures...))
	}
	fmt.Fprintln(output, notice)
	return nil
}

// ensureImage keeps cached images only when a toolchain image uses the current base.
func ensureImage(p podmanTarget, set []string, output io.Writer) (string, error) {
	hash, err := image.Hash(image.Context())
	if err != nil {
		return "", err
	}
	baseTag := image.Tag(hash, nil)
	baseExists, err := p.exists(imageResource, baseTag)
	if err != nil {
		return "", err
	}
	if !baseExists {
		if _, err := buildLabeledImage(p, nil, hash, "", false, output); err != nil {
			return "", err
		}
	}
	if len(set) == 0 {
		return baseTag, nil
	}
	base, err := inspectImage(p, baseTag)
	if err != nil {
		return "", err
	}
	tag := image.Tag(hash, set)
	exists, err := p.exists(imageResource, tag)
	if err != nil {
		return "", err
	}
	if exists {
		current, err := inspectImage(p, tag)
		if err != nil {
			return "", err
		}
		if current.Labels[baseImageLabel] == base.ID {
			return tag, nil
		}
	}
	return buildLabeledImage(p, set, hash, base.ID, false, output)
}

func sandboxImageOutdated(p podmanTarget, container sandboxContainer, hash string) (bool, error) {
	set, err := sandboxToolchains(container)
	if err != nil {
		return false, err
	}
	tag := image.Tag(hash, set)
	exists, err := p.exists(imageResource, tag)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	current, err := inspectImage(p, tag)
	if err != nil {
		return false, err
	}
	if strings.TrimPrefix(current.ID, "sha256:") != strings.TrimPrefix(container.Image, "sha256:") {
		return true, nil
	}
	if len(set) == 0 {
		return false, nil
	}
	baseTag := image.Tag(hash, nil)
	exists, err = p.exists(imageResource, baseTag)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	base, err := inspectImage(p, baseTag)
	if err != nil {
		return false, err
	}
	return current.Labels[baseImageLabel] != base.ID, nil
}
