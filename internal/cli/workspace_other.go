//go:build !windows

package cli

import "path/filepath"

// resolveExistingPath resolves symbolic links in an existing path.
func resolveExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
