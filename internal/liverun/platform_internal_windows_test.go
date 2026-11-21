package liverun

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVirtualDiskReadsBasePathLiterally proves the WSL disk lookup finds the one
// ext4.vhdx in a base directory whose name holds pattern characters, and still
// refuses a directory with two disks. It exercises the virtual disk discovery seam.
func TestVirtualDiskReadsBasePathLiterally(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Lab[1]")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "ext4.vhdx")
	if err := os.WriteFile(want, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := virtualDisk(base); err != nil || got != want {
		t.Fatalf("virtualDisk = %q, %v; want %q", got, err, want)
	}
	if err := os.WriteFile(filepath.Join(base, "other.vhdx"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := virtualDisk(base); err == nil {
		t.Fatalf("two disks accepted: %q", got)
	}
}
