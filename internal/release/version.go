// Package release supplies shared build metadata and normalized context preparation.
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
