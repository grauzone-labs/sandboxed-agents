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
const ReleaseVersion = "0.0.30-test"

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
	err := image.WriteContextAndVisit(source, directory, func(name string) error {
		return RewriteFileToCRLF(directory, name)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// RewriteFileToCRLF rewrites one text fixture file with Windows line endings, preserving binary bytes.
func RewriteFileToCRLF(directory, name string) error {
	file := filepath.Join(directory, filepath.FromSlash(name))
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if image.IsBinary(name, data) {
		return nil
	}
	return os.WriteFile(file, ToCRLF(image.Normalize(name, data)), 0600)
}
