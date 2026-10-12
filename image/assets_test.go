package image_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// expectedFingerprint records the independently documented pin in docs/images.md.
const expectedFingerprint = "6F71F525282841EEDAF851B42F59B5F99B1BE0B4\n"

// TestEmbeddedFingerprintIsNormalized proves the pinned fingerprint is embedded and written with LF from LF and CRLF sources through the public context and writing APIs.
func TestEmbeddedFingerprintIsNormalized(t *testing.T) {
	const name = "base/nodesource-signing-key.fingerprint"
	embedded, err := fs.ReadFile(image.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != expectedFingerprint {
		t.Fatalf("embedded fingerprint = %q; want %q", embedded, expectedFingerprint)
	}
	crlf := t.TempDir()
	releasetest.CopyCRLFContext(t, image.Context(), crlf)
	raw, err := os.ReadFile(filepath.Join(crlf, filepath.FromSlash(name)))
	if err != nil || !bytes.Contains(raw, []byte("\r\n")) {
		t.Fatalf("CRLF fixture fingerprint = %q, error = %v", raw, err)
	}
	for _, source := range []fs.FS{image.Context(), os.DirFS(crlf)} {
		directory := t.TempDir()
		if err := image.WriteContext(source, directory); err != nil {
			t.Fatal(err)
		}
		written, err := os.ReadFile(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil || string(written) != expectedFingerprint {
			t.Fatalf("written fingerprint = %q, error = %v; want %q", written, err, expectedFingerprint)
		}
	}
}

// TestAssetHashIgnoresWindowsLineEndings proves that a CRLF checkout has the LF asset hash through the public image hash API.
func TestAssetHashIgnoresWindowsLineEndings(t *testing.T) {
	original := os.DirFS("context")
	dir := t.TempDir()
	releasetest.CopyCRLFContext(t, original, dir)
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

// TestFingerprintEditChangesAssetHash proves that editing the pinned signing key fingerprint file changes the asset hash through the public image hash API.
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
