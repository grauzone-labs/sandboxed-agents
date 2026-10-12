package testutil

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// ExpectedVersion specifies the version command's output independently of the production formatter.
func ExpectedVersion(version, assetHash string) string {
	return fmt.Sprintf("sandboxed-agents %s\nasset-sha256 %s\n", version, assetHash)
}

// CopyCRLFContext copies image build context text with Windows line endings and preserves binary assets.
func CopyCRLFContext(t *testing.T, source fs.FS, directory string) {
	t.Helper()
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		if path.Ext(name) != image.BinarySuffix && !bytes.Contains(data, []byte{0}) {
			data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		}
		file := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
