package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func validateWorkspace(path string) error {
	return validateWorkspaceForHost(path, runtime.GOOS)
}

func validateWorkspaceForHost(path, hostOS string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace %q is not a directory", path)
	}
	workspace, err := resolvedPath(path)
	if err != nil {
		return fmt.Errorf("workspace %q: %w", path, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var stateRoot, localAppData string
	if hostOS == "windows" {
		localAppData = os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			return fmt.Errorf("LOCALAPPDATA is required to identify protected host state")
		}
		stateRoot = filepath.Join(localAppData, "sandboxed-agents")
	} else {
		stateRoot, err = hostStateRoot(hostOS)
		if err != nil {
			return err
		}
	}
	protectedPaths := []string{executable, filepath.Join(home, ".ssh"), stateRoot}
	if hostOS == "windows" {
		protectedPaths = append(protectedPaths, filepath.Join(localAppData, "Programs", "sandboxed-agents"))
	}
	// Other package launchers and temporary build inputs join this set when those
	// later command bodies are implemented.
	for _, path := range protectedPaths {
		protected, err := resolvedPath(path)
		if err != nil {
			return fmt.Errorf("protected host path %q: %w", path, err)
		}
		if within(workspace, protected) || within(protected, workspace) {
			return fmt.Errorf("workspace overlaps protected host path %q", path)
		}
	}
	return nil
}

// Resolve existing ancestors too: protected host paths may not have been created.
func resolvedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
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

func within(path, parent string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		parent = strings.ToLower(parent)
	}
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
