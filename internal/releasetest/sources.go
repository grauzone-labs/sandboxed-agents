package releasetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// CopyRepositorySources copies the selected release build sources with LF line endings, excluding image context assets.
func CopyRepositorySources(t *testing.T, root, destination string) {
	t.Helper()
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == filepath.Join("image", "context") || relative != "." && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isReleaseSource(filepath.ToSlash(relative)) {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		data = image.Normalize(filepath.ToSlash(relative), data)
		file := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// isReleaseSource mirrors scripts/release.sh's module, Go commands, script and linker metadata inputs, plus checkout attributes.
func isReleaseSource(name string) bool {
	return name == "go.mod" || name == ".gitattributes" || strings.HasSuffix(name, ".go") || name == "scripts/release.sh" || name == "scripts/version-symbol.txt"
}
