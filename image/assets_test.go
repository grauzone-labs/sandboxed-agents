package image_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestAssetHashIgnoresWindowsLineEndings proves that a CRLF checkout has the LF asset hash and embedded text has no CR bytes through the public image APIs.
func TestAssetHashIgnoresWindowsLineEndings(t *testing.T) {
	original := os.DirFS("context")
	dir := t.TempDir()
	testutil.CopyCRLFContext(t, original, dir)
	err := fs.WalkDir(image.Context(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(image.Context(), name)
		if err != nil {
			return err
		}
		if filepath.Ext(name) != image.BinarySuffix && !bytes.Contains(data, []byte{0}) && bytes.Contains(data, []byte("\r")) {
			t.Errorf("embedded text asset %s contains CR bytes", name)
		}
		return nil
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
		t.Fatalf("CRLF asset hash = %s; LF asset hash = %s", got, want)
	}
}

// TestWriteContextNormalizesTextAndPreservesBinary proves text is written with LF while suffix-marked and NUL-containing binary bytes remain intact through the public context-writing API.
func TestWriteContextNormalizesTextAndPreservesBinary(t *testing.T) {
	source := fstest.MapFS{
		"base/recipe":                  {Data: []byte("one\r\ntwo\r\n")},
		"manager" + image.BinarySuffix: {Data: []byte{255, '\r', '\n'}},
		"opaque.data":                  {Data: []byte{0, '\r', '\n', 255}},
	}
	directory := t.TempDir()
	if err := image.WriteContext(source, directory); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		"base/recipe":                  []byte("one\ntwo\n"),
		"manager" + image.BinarySuffix: {255, '\r', '\n'},
		"opaque.data":                  {0, '\r', '\n', 255},
	} {
		got, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("written asset %s = %v; want %v", name, got, want)
		}
	}
}

// TestAssetHashPreservesNULBinaryBytes proves that NUL-containing assets without the binary suffix retain CRLF bytes through the public asset hash API.
func TestAssetHashPreservesNULBinaryBytes(t *testing.T) {
	context := fstest.MapFS{"opaque.data": {Data: []byte{0, '\r', '\n', 255}}}
	got, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	const want = "008bfbbcf48de0d68229dea98bd76093c011c2cd271ef475971f1625aba4f3bd"
	if got != want {
		t.Fatalf("asset hash = %s; want unchanged NUL-containing binary encoding %s", got, want)
	}
}

// TestAssetHashSortsCompleteRelativePaths proves that files sort globally rather than by directory traversal through the public image hash API.
func TestAssetHashSortsCompleteRelativePaths(t *testing.T) {
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
		t.Fatalf("asset hash = %s; want globally sorted encoding %s", got, want)
	}
}

// TestBinaryContextAssetsRetainBytes proves that BinarySuffix assets without NUL retain CRLF bytes in the asset hash through the public image hash API.
func TestBinaryContextAssetsRetainBytes(t *testing.T) {
	context := fstest.MapFS{"manager" + image.BinarySuffix: {Data: []byte{255, '\r', '\n'}}}
	before, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	context["manager"+image.BinarySuffix].Data = []byte{255, '\n'}
	after, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("binary CRLF bytes were normalized")
	}
}

// TestFingerprintEditChangesAssetHash proves that editing the pinned signing key changes the asset hash through the public image hash API.
func TestFingerprintEditChangesAssetHash(t *testing.T) {
	directory := t.TempDir()
	if err := image.WriteContext(image.Context(), directory); err != nil {
		t.Fatal(err)
	}
	context := os.DirFS(directory)
	before, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "base", "nodesource-signing-key.fingerprint"), []byte("0000000000000000000000000000000000000000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("editing the signing key fingerprint did not change the asset hash")
	}
}

// TestAssetHashUsesFramedPaths proves the documented asset hash encoding and rename sensitivity through the public image hash API.
func TestAssetHashUsesFramedPaths(t *testing.T) {
	context := fstest.MapFS{"base/example.sh": {Data: []byte("one\r\ntwo\r\n")}}
	got, err := image.Hash(context)
	if err != nil {
		t.Fatal(err)
	}
	const want = "a25ef61c2fbd6d4e034154492b542f255f949e8d779163f2cde86c34b54a577d"
	if got != want {
		t.Fatalf("asset hash = %s; want worked encoding %s", got, want)
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
