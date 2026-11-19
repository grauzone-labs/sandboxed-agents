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

// toolchainSet formats a toolchain set for labels, using none for an empty set.
func toolchainSet(set []string) string {
	if len(set) == 0 {
		return "none"
	}
	return strings.Join(set, ",")
}

// sandboxToolchains reads and validates a sandbox's recorded toolchains, falling back to its
// image tag.
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

// recordedToolchains validates, sorts, and deduplicates a recorded toolchain selection.
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

// inspectImage reads exactly one image record with a nonempty immutable ID.
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

// buildLabeledImage builds an image with product, asset, toolchain, and base-image labels and
// the requested refresh policy.
func buildLabeledImage(p podmanTarget, set []string, hash, baseID string, refresh bool, output, progress io.Writer) (string, error) {
	lock, err := acquireImageTagLock(p, image.Tag(hash, set), progress)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	return buildLabeledImageUnderLock(p, set, hash, baseID, refresh, output)
}

// buildLabeledImageUnderLock builds a tag while its caller holds that tag's exclusive image-tag lock.
func buildLabeledImageUnderLock(p podmanTarget, set []string, hash, baseID string, refresh bool, output io.Writer) (string, error) {
	return buildLabeledImageWithRunner(p, set, hash, baseID, refresh, func(args ...string) error {
		result, err := p.query(args...)
		if result != "" {
			fmt.Fprint(output, result)
		}
		return err
	})
}

// buildLabeledImageWithRunner applies identical build flags and evidence publication to serial and concurrent runners under the caller's image-tag lock.
func buildLabeledImageWithRunner(p podmanTarget, set []string, hash, baseID string, refresh bool, run func(...string) error) (string, error) {
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
		return withBuildEvidence(p, image.Tag(hash, set), hash, func(iid, layerLabel string) error {
			buildArgs := args
			if iid != "" {
				buildArgs = append(append(append([]string{}, args[:len(args)-1]...), "--iidfile", iid), args[len(args)-1])
			}
			if layerLabel != "" {
				buildArgs = append(append(append([]string{}, buildArgs[:len(buildArgs)-1]...), "--layer-label", layerLabel), buildArgs[len(buildArgs)-1])
			}
			return run(buildArgs...)
		})
	})
}

// rebuildImages discovers images from labels before rebuilding, preserving tags on failed builds.
func rebuildImages(request Request, p podmanTarget, output, progress io.Writer) error {
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
	// Capture the base's immutable ID before another tag writer can replace it.
	const notice = "existing sandboxes keep their image until update; list marks them as outdated"
	var baseOutputErr error
	baseID, err := func() (string, error) {
		lock, err := acquireImageTagLock(p, image.Tag(hash, nil), progress)
		if err != nil {
			return "", err
		}
		defer lock.Close()
		var outcome imageBuildOutcome
		if request.BuildJobs > 1 {
			outcome = (imageRebuildContext{target: p, hash: hash}).rebuildBaseWithProgress(output, progress)
		} else {
			outcome.tag, outcome.buildErr = buildLabeledImageUnderLock(p, nil, hash, "", true, output)
		}
		baseOutputErr = outcome.outputErr
		if outcome.buildErr != nil {
			return "", outcome.buildErr
		}
		if request.BuildJobs <= 1 {
			fmt.Fprintln(output, "built image", outcome.tag)
		}
		if len(sets) == 0 {
			return "", nil
		}
		base, err := inspectImage(p, outcome.tag)
		if err != nil {
			return "", fmt.Errorf("%w; %s", err, notice)
		}
		return base.ID, nil
	}()
	if err != nil {
		return errors.Join(err, baseOutputErr)
	}
	if len(sets) == 0 {
		fmt.Fprintln(output, notice)
		if baseOutputErr != nil {
			return fmt.Errorf("build output errors; %s: %w", notice, baseOutputErr)
		}
		return nil
	}
	var names []string
	for name := range sets {
		names = append(names, name)
	}
	slices.Sort(names)
	var failureCauses []error
	var failedSets []string
	buildFailures := make([]error, len(names))
	outputErr := baseOutputErr
	if request.BuildJobs > 1 {
		var toolchainOutputErr error
		buildFailures, toolchainOutputErr = (imageRebuildContext{target: p, hash: hash, baseID: baseID}).rebuildToolchains(sets, names, request.BuildJobs, output, progress)
		outputErr = errors.Join(outputErr, toolchainOutputErr)
	} else {
		for i, name := range names {
			tag, err := buildLabeledImage(p, sets[name], hash, baseID, true, output, progress)
			buildFailures[i] = err
			if err == nil {
				fmt.Fprintln(output, "built image", tag)
			}
		}
	}
	for i, name := range names {
		err := buildFailures[i]
		if err != nil {
			failureCauses = append(failureCauses, err)
			failedSets = append(failedSets, name)
		}
	}
	if len(failureCauses) > 0 {
		return errors.Join(fmt.Errorf("failed toolchain sets: %s; %s: %w", strings.Join(failedSets, "; "), notice, errors.Join(failureCauses...)), outputErr)
	}
	fmt.Fprintln(output, notice)
	if outputErr != nil {
		return fmt.Errorf("build output errors; %s: %w", notice, outputErr)
	}
	return nil
}

// ensureImage keeps cached images only when a toolchain image uses the current base.
func ensureImage(p podmanTarget, set []string, output, progress io.Writer) (string, error) {
	hash, err := image.Hash(image.Context())
	if err != nil {
		return "", err
	}
	// Take the toolchain lock before reading the base ID: a waiter must judge the
	// completed toolchain build against the base that is current after its wait.
	if len(set) > 0 {
		lock, err := acquireImageTagLock(p, image.Tag(hash, set), progress)
		if err != nil {
			return "", err
		}
		defer lock.Close()
	}
	baseTag, baseID, err := ensureBaseImage(p, hash, len(set) > 0, output, progress)
	if err != nil {
		return "", err
	}
	if len(set) == 0 {
		return baseTag, nil
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
		if current.Labels[baseImageLabel] == baseID {
			return tag, nil
		}
	}
	return buildLabeledImageUnderLock(p, set, hash, baseID, false, output)
}

// ensureBaseImage checks or builds the base tag under its image-tag lock, returning its immutable
// ID only when a toolchain build needs it. A toolchain ensure already holds its toolchain lock;
// explicit rebuilds release the base lock before taking a toolchain lock, avoiding a cycle.
func ensureBaseImage(p podmanTarget, hash string, inspect bool, output, progress io.Writer) (string, string, error) {
	tag := image.Tag(hash, nil)
	lock, err := acquireImageTagLock(p, tag, progress)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	exists, err := p.exists(imageResource, tag)
	if err != nil {
		return "", "", err
	}
	if !exists {
		if _, err := buildLabeledImageUnderLock(p, nil, hash, "", false, output); err != nil {
			return "", "", err
		}
	}
	if !inspect {
		return tag, "", nil
	}
	base, err := inspectImage(p, tag)
	return tag, base.ID, err
}

// sandboxImageOutdated compares a sandbox's image with the current image for its recorded
// toolchains.
func sandboxImageOutdated(p podmanTarget, container sandboxContainer, hash string) (bool, error) {
	set, err := sandboxToolchains(container)
	if err != nil {
		return false, err
	}
	return imageOutdated(p, container.Image, set, hash)
}

// imageOutdated reports whether an image differs from the current tag or uses an outdated base
// image.
func imageOutdated(p podmanTarget, imageID string, set []string, hash string) (bool, error) {
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
	if strings.TrimPrefix(current.ID, "sha256:") != strings.TrimPrefix(imageID, "sha256:") {
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
