package image_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestContextHashIgnoresWindowsLineEndings proves that an actual CRLF checkout has the LF context's identity through the public image hash API.
func TestContextHashIgnoresWindowsLineEndings(t *testing.T) {
	original := os.DirFS("context")
	dir := t.TempDir()
	err := fs.WalkDir(original, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(original, path)
		if err != nil {
			return err
		}
		name := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if filepath.Ext(name) != ".bin" {
			data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
			data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		}
		return os.WriteFile(name, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := image.Hash(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := image.Hash(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("CRLF context hash = %s; LF context hash = %s", got, want)
	}
}

// TestContextHashSortsCompleteRelativePaths proves that files sort globally rather than by directory traversal through the public image hash API.
func TestContextHashSortsCompleteRelativePaths(t *testing.T) {
	context := fstest.MapFS{
		"a/nested": {Data: []byte("nested\n")},
		"a.txt":    {Data: []byte("root\n")},
	}
	got, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	// The worked encoding places a.txt before a/nested, framing each path and content with uint64 big-endian lengths.
	const want = "c5bb9db3bc4ea9f601dcae2c6ac22dddb88f720a759cb2801cb37d3c27d260a3"
	if got != want {
		t.Fatalf("context hash = %s; want globally sorted encoding %s", got, want)
	}
}

// TestBinaryContextAssetsRetainBytes proves that the .bin convention includes CRLF bytes in the identity through the public image hash API.
func TestBinaryContextAssetsRetainBytes(t *testing.T) {
	context := fstest.MapFS{"manager.bin": {Data: []byte{0, '\r', '\n', 255}}}
	before, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	context["manager.bin"].Data = []byte{0, '\n', 255}
	after, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("binary CRLF bytes were normalized")
	}
}

// TestFingerprintEditChangesContextHash proves that editing the pinned signing key changes the asset identity through the public image hash API.
func TestFingerprintEditChangesContextHash(t *testing.T) {
	context := fstest.MapFS{}
	err := fs.WalkDir(image.Context(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(image.Context(), path)
		if err != nil {
			return err
		}
		context[path] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	context["base/nodesource-signing-key.fingerprint"].Data = []byte("0000000000000000000000000000000000000000\n")
	after, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("editing the signing key fingerprint did not change the context hash")
	}
}

// TestContextHashUsesFramedPaths proves the documented fingerprint encoding and rename sensitivity through the public image hash API.
func TestContextHashUsesFramedPaths(t *testing.T) {
	context := fstest.MapFS{"base/example.sh": {Data: []byte("one\r\ntwo\r\n")}}
	got, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	const want = "a25ef61c2fbd6d4e034154492b542f255f949e8d779163f2cde86c34b54a577d"
	if got != want {
		t.Fatalf("context hash = %s; want worked encoding %s", got, want)
	}
	context["base/renamed.sh"] = context["base/example.sh"]
	delete(context, "base/example.sh")
	renamed, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if renamed == got {
		t.Fatal("renaming a context asset did not change its hash")
	}
}
