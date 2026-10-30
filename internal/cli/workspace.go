package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// podmanWorkspacePath returns the optional workspace bind path, translating Windows drive paths
// for WSL.
func podmanWorkspacePath(request Request, p podmanTarget) (string, error) {
	if len(request.Args) != 2 {
		return "", nil
	}
	if p.host.Platform == "windows" {
		return windowsWorkspacePath(request.Args[1])
	}
	return request.Args[1], nil
}

// checkWorkspaceAutomount rejects Windows workspace binds when the selected machine uses a
// custom automount root.
func checkWorkspaceAutomount(request Request, p podmanTarget) error {
	if len(request.Args) == 2 && p.host.Platform == "windows" && p.automountRoot != "/mnt/" && p.automountRoot != "/mnt" {
		return fmt.Errorf("workspace binds are unsupported with custom WSL automount root %q on machine %q", p.automountRoot, p.connection)
	}
	return nil
}

// validateWorkspaceForHost checks that the workspace resolves to a directory outside protected
// host paths.
func validateWorkspaceForHost(path, hostOS string) error {
	_, err := workspacePathForHost(path, hostOS)
	return err
}

// workspacePathForHost resolves a workspace directory and rejects overlap with protected host
// paths.
func workspacePathForHost(path, hostOS string) (string, error) {
	resolved, err := resolvedPath(path)
	if err != nil {
		return "", fmt.Errorf("workspace %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory", resolved)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", hostStateError{err}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", hostStateError{err}
	}
	stateRoot, err := hostStateRoot(hostOS)
	if err != nil {
		return "", err
	}
	protectedPaths := []string{executable, filepath.Join(home, ".ssh"), stateRoot}
	if hostOS == "windows" {
		protectedPaths = append(protectedPaths, filepath.Join(filepath.Dir(stateRoot), "Programs", "sandboxed-agents"))
	}
	launchers, err := packageLaunchers(executable)
	if err != nil {
		return "", err
	}
	protectedPaths = append(protectedPaths, launchers...)
	buildRoot := os.TempDir()
	resolvedBuildRoot, err := resolvedPath(buildRoot)
	if err != nil {
		return "", fmt.Errorf("protected host path %q: %w", buildRoot, err)
	}
	// Builds allocate their inputs under this root after Parse. Refuse its
	// ancestors, while allowing ordinary workspace directories below it.
	if within(resolvedBuildRoot, resolved) {
		return "", fmt.Errorf("workspace overlaps protected host path %q (temporary build root)", buildRoot)
	}
	entries, err := os.ReadDir(buildRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read protected temporary build inputs in %q: %w", buildRoot, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "sandboxed-agents-build-") {
			protectedPaths = append(protectedPaths, filepath.Join(buildRoot, entry.Name()))
		}
	}
	for _, path := range protectedPaths {
		protected, err := resolvedPath(path)
		if err != nil {
			return "", fmt.Errorf("protected host path %q: %w", path, err)
		}
		// A launcher can itself be a symlink outside its install directory.
		// Protect both its target and the directory entry users invoke.
		parent, err := resolvedPath(filepath.Dir(path))
		if err != nil {
			return "", fmt.Errorf("protected host path %q: %w", path, err)
		}
		entry := filepath.Join(parent, filepath.Base(path))
		if within(resolved, protected) || within(protected, resolved) || within(entry, resolved) {
			return "", fmt.Errorf("workspace overlaps protected host path %q", path)
		}
	}
	return resolved, nil
}

// packageLaunchers collects protected package launcher paths from installation metadata and
// executable search directories.
func packageLaunchers(executable string) ([]string, error) {
	directories := append([]string{filepath.Dir(executable)}, filepath.SplitList(os.Getenv("PATH"))...)
	var paths []string
	// The npm launcher knows its own location and the links/shims created by
	// npm, even when invoked directly with their directories absent from PATH.
	if encoded := os.Getenv("SANDBOXED_AGENTS_NPM_LAUNCHERS"); encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
			return nil, fmt.Errorf("protected npm launcher paths: %w", err)
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("protected npm launcher path %q must be absolute", path)
			}
		}
	}
	for _, directory := range directories {
		for _, name := range []string{"sandboxed-agents", "sandboxed-agents.exe", "sandboxed-agents.cmd", "sandboxed-agents.ps1"} {
			path := filepath.Join(directory, name)
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("protected package launcher %q: %w", path, err)
			}
			if !info.IsDir() {
				paths = append(paths, path)
			}
		}
	}
	return paths, nil
}

// Resolve existing ancestors too: protected host paths may not have been created.
func resolvedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := resolveExistingPath(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = resolvedPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

// within reports whether a path is inside or equal to a parent, folding case on Windows.
func within(path, parent string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		parent = strings.ToLower(parent)
	}
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Windows binds address a resolved local drive through WSL's default automount.
// UNC paths have no corresponding drive mount and must not be guessed.
func windowsWorkspacePath(path string) (string, error) {
	normalized := strings.ReplaceAll(path, `\`, "/")
	normalized = strings.TrimPrefix(normalized, "//?/")
	if len(normalized) < 3 || normalized[1:3] != ":/" || !((normalized[0] >= 'A' && normalized[0] <= 'Z') || (normalized[0] >= 'a' && normalized[0] <= 'z')) {
		return "", fmt.Errorf("workspace %q must be on an absolute Windows drive path for WSL", path)
	}
	return "/mnt/" + strings.ToLower(normalized[:1]) + "/" + normalized[3:], nil
}

// normalizeWindowsFinalPath normalizes extended Windows drive and UNC paths and rejects
// unsupported final-path forms.
func normalizeWindowsFinalPath(path string) (string, error) {
	if strings.HasPrefix(path, `\\?\UNC\`) {
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	} else {
		path = strings.TrimPrefix(path, `\\?\`)
	}
	if len(path) >= 3 && path[1:3] == `:\` && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
		return path, nil
	}
	if strings.HasPrefix(path, `\\`) {
		parts := strings.Split(strings.TrimPrefix(path, `\\`), `\`)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("unsupported Windows final path %q", path)
}
