package image

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Build writes the image assets, builds the base Containerfile with Podman, and removes the temporary context.
// The caller supplies a Podman runner bound to the target checked by its preflight.
// A nil source uses Context. Rebuild flags and image labels belong to the caller's runner.
// It returns the image tag only when the build and cleanup both succeed.
func Build(ctx context.Context, source fs.FS, podman func(context.Context, ...string) error) (string, error) {
	return BuildWithToolchains(ctx, source, nil, podman)
}

// BuildWithToolchains generates a recipe for the caller's validated toolchain set.
// Nonempty sets layer the selected fragments on the base image. Like Build,
// it returns a tag only when the Podman build and context cleanup both succeed.
func BuildWithToolchains(ctx context.Context, source fs.FS, toolchains []string, podman func(context.Context, ...string) error) (tag string, err error) {
	if podman == nil {
		return "", fmt.Errorf("image build requires a checked Podman runner")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if source == nil {
		source = Context()
	}
	directory, err := os.MkdirTemp("", "sandboxed-agents-build-")
	if err != nil {
		return "", fmt.Errorf("create image build context: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove image build context: %w", cleanupErr))
			tag = ""
		}
	}()
	if err := WriteContext(source, directory); err != nil {
		return "", fmt.Errorf("write image build context: %w", err)
	}
	// Hash the files sent to Podman, so even a mutable source is read only once.
	written := os.DirFS(directory)
	hash, err := Hash(written)
	if err != nil {
		return "", fmt.Errorf("hash image build context: %w", err)
	}
	containerfile := "base/Containerfile"
	info, err := fs.Stat(written, containerfile)
	if err != nil {
		return "", fmt.Errorf("image Containerfile %s: %w", containerfile, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("image Containerfile %s is not a regular file", containerfile)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	imageTag := Tag(hash, toolchains)
	buildDirectory := filepath.Join(directory, "base")
	if len(toolchains) != 0 {
		containerfile = "toolchains/Containerfile"
		if err := writeToolchainRecipe(directory, hash, toolchains); err != nil {
			return "", err
		}
		buildDirectory = directory
	}
	if err := podman(ctx, "build", "--file", filepath.Join(directory, filepath.FromSlash(containerfile)), "--tag", imageTag, buildDirectory); err != nil {
		return "", fmt.Errorf("build image %s: %w", imageTag, err)
	}
	return imageTag, nil
}

// writeToolchainRecipe assembles canonical fragments after hashing the original assets.
func writeToolchainRecipe(directory, hash string, toolchains []string) error {
	set := slices.Clone(toolchains)
	slices.Sort(set)
	set = slices.Compact(set)
	var recipe strings.Builder
	fmt.Fprintf(&recipe, "ARG BASE_IMAGE=%s\nFROM ${BASE_IMAGE}\n", Tag(hash, nil))
	for _, name := range set {
		// Although callers validate names, keep recipe paths confined to their asset directory.
		if name == "" || strings.ContainsAny(name, `/\\.`) {
			return fmt.Errorf("invalid toolchain recipe %q", name)
		}
		fragment, err := os.ReadFile(filepath.Join(directory, "toolchains", name+".Containerfile"))
		if err != nil {
			return fmt.Errorf("read toolchain recipe %s: %w", name, err)
		}
		fmt.Fprintf(&recipe, "\n# Toolchain: %s\n%s\n", name, fragment)
	}
	recipe.WriteString("\nRUN /usr/local/bin/sandboxed-agents-manager record-versions\n")
	return os.WriteFile(filepath.Join(directory, "toolchains", "Containerfile"), []byte(recipe.String()), 0600)
}

// Tag identifies an asset hash and a validated toolchain set independently of controller groups.
// The caller supplies a SHA256 asset hash and a set accepted by the command's catalog validation.
func Tag(assetHash string, toolchains []string) string {
	set := slices.Clone(toolchains)
	slices.Sort(set)
	set = slices.Compact(set)
	name := strings.Join(set, "-")
	if name == "" {
		name = "none"
	}
	return "localhost/sandboxed-agents:" + name + "-" + assetHash
}
