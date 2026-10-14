// Package releasetest supplies test-owned release fixtures and assertions.
package releasetest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

var preparedHash struct {
	sync.Once
	hash string
	err  error
}

// ExpectedPreparedVersion specifies the release version output using the context with its compiled manager.
func ExpectedPreparedVersion(t *testing.T, version string) string {
	t.Helper()
	preparedHash.Do(func() {
		root, err := os.Getwd()
		if err != nil {
			preparedHash.err = err
			return
		}
		for {
			if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				preparedHash.err = fmt.Errorf("cannot find repository root")
				return
			}
			root = parent
		}
		directory, err := os.MkdirTemp("", "sandboxed-agents-expected-overlay-")
		if err != nil {
			preparedHash.err = err
			return
		}
		defer os.RemoveAll(directory)
		command := exec.Command("go", "run", "./image/cmd/overlay", directory)
		command.Dir = root
		command.Env = append(os.Environ(), "GOOS=", "GOARCH=", "GOTOOLCHAIN=local", "GOFLAGS=", "GOEXPERIMENT=", "GOWORK=off", "GOENV=off", "GOFIPS140=off")
		if output, err := command.CombinedOutput(); err != nil {
			preparedHash.err = fmt.Errorf("expected release context: %w\n%s", err, output)
			return
		}
		preparedHash.hash, preparedHash.err = image.Hash(os.DirFS(filepath.Join(directory, "context")))
	})
	if preparedHash.err != nil {
		t.Fatal(preparedHash.err)
	}
	return fmt.Sprintf("sandboxed-agents %s\nasset-sha256 %s\n", version, preparedHash.hash)
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
