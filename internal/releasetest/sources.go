package releasetest

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if relative != "go.mod" && relative != ".gitattributes" && !strings.HasSuffix(relative, ".go") && relative != filepath.Join("scripts", "release.sh") && relative != filepath.Join("scripts", "version-symbol.txt") {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
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
