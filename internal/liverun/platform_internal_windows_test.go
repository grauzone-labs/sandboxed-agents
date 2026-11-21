package liverun

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestWriteJSONOutlastsOpenReader proves artifact publication succeeds while a
// reader briefly holds the destination open through os.Open, which grants no
// delete sharing on Windows. It exercises the writeJSON publication seam.
func TestWriteJSONOutlastsOpenReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	if err := writeJSON(path, activeRun{RunDir: "first"}); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writeJSON(path, activeRun{RunDir: "second"}) }()
	time.Sleep(50 * time.Millisecond)
	reader.Close()
	if err = <-done; err != nil {
		t.Fatalf("open reader interrupted publication: %v", err)
	}
	var active activeRun
	if err = readJSON(path, &active); err != nil || active.RunDir != "second" {
		t.Fatalf("published record = %+v, %v", active, err)
	}
}
