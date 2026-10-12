// Package releasetest supplies test-owned release fixtures and assertions.
package releasetest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// ExpectedVersion specifies the version output and asset hash independently of the production formatter.
func ExpectedVersion(t *testing.T, version string) string {
	t.Helper()
	assetHash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sandboxed-agents %s\nasset-sha256 %s\n", version, assetHash)
}

// CopyCRLFContext copies image build context text with Windows line endings and preserves binary assets.
func CopyCRLFContext(t *testing.T, source fs.FS, directory string) {
	t.Helper()
	if err := image.WriteContext(source, directory); err != nil {
		t.Fatal(err)
	}
	context := os.DirFS(directory)
	err := fs.WalkDir(context, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(context, name)
		if err != nil {
			return err
		}
		if image.IsBinary(name, data) {
			return nil
		}
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		file := filepath.Join(directory, filepath.FromSlash(name))
		return os.WriteFile(file, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
