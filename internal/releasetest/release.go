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

// ReleaseVersion is the injected version used by release tests.
const ReleaseVersion = "v0.0.30-test"

// ToCRLF converts LF-normalized text to Windows line endings.
func ToCRLF(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
}

// ExpectedVersion specifies the output format independently of the production formatter, using the shared image asset hash.
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
	context := os.DirFS(directory)
	err := image.WriteContextAndVisit(source, directory, func(name string) error {
		data, err := fs.ReadFile(context, name)
		if err != nil {
			return err
		}
		if image.IsBinary(name, data) {
			return nil
		}
		data = ToCRLF(data)
		file := filepath.Join(directory, filepath.FromSlash(name))
		return os.WriteFile(file, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
