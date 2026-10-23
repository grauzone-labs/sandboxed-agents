package manualcheck

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"testing"
)

// writeTarball writes a gzipped tarball with the given members.
func writeTarball(t *testing.T, path string, members map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zipper := gzip.NewWriter(file)
	writer := tar.NewWriter(zipper)
	for name, content := range members {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []interface{ Close() error }{writer, zipper, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
