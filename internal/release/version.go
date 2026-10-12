// Package release supplies the canonical version-symbol metadata for release builds.
package release

import (
	"io/fs"
	"strings"
)

// VersionSymbol reads the canonical linker target from the repository metadata.
func VersionSymbol(repository fs.FS) (string, error) {
	data, err := fs.ReadFile(repository, "scripts/version-symbol.txt")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
