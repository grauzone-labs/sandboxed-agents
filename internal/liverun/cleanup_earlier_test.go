package liverun

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestEarlierCleanupNamesBoundsTheScan proves that the earlier-cleanup scan reads no more run-directory entries than
// its limit, across several directory chunks, and returns only real attempt directories other than the excluded one.
// It exercises the earlierCleanupNames scan seam over a real directory held through os.Root.
func TestEarlierCleanupNamesBoundsTheScan(t *testing.T) {
	directory := t.TempDir()
	for i := range 150 {
		if err := os.Mkdir(filepath.Join(directory, fmt.Sprintf("cleanup-%03d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "cleanup-file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if names := earlierCleanupNames(root, "", 100); len(names) > 100 || len(names) < 99 {
		t.Fatalf("scan of 100 entries returned %d names", len(names))
	}
	names := earlierCleanupNames(root, "cleanup-007", 1000)
	if len(names) != 149 {
		t.Fatalf("full scan returned %d names, want 149", len(names))
	}
	for _, name := range names {
		if name == "cleanup-007" || name == "cleanup-file" {
			t.Fatalf("scan returned %s", name)
		}
	}
}
