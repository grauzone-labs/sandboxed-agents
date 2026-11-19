package liverun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPeakSamplingRecordsFactsWithoutBound proves filesystem minima are retained separately from admissible future headroom. It exercises the injected filesystem and peak artifacts seam.
func TestPeakSamplingRecordsFactsWithoutBound(t *testing.T) {
	root := t.TempDir()
	o := Options{RunDir: filepath.Join(root, "private"), LockRoot: filepath.Join(root, "locks"), Revision: strings.Repeat("a", 40)}
	for _, directory := range []string{o.RunDir, o.LockRoot} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	before := Snapshot{EngineID: "engine:/storage", Target: Target{Storage: "/storage"}, Disks: []Disk{{Name: "engine", Path: "/storage", FreeBytes: 900}}, TakenAt: time.Now().UTC()}
	service := &Service{Platform: "linux", FreeSpace: func(string) (uint64, error) { return 700, nil }}
	finish := service.samplePeak(context.Background(), o, before)
	var ongoing PeakRecord
	if err := readJSON(observedPeakPath(o, before.EngineID), &ongoing); err != nil || ongoing.SamplingComplete {
		t.Fatalf("ongoing sample absent or falsely complete: %v %+v", err, ongoing)
	}
	if !finish() {
		t.Fatal("could not retain peak facts")
	}
	var peak PeakRecord
	if err := readJSON(observedPeakPath(o, before.EngineID), &peak); err != nil {
		t.Fatal(err)
	}
	if !peak.SamplingComplete || len(peak.Filesystems) != 1 || peak.Filesystems[0].SampledGrowthBytes != 200 || peak.PeakAdditionalBytes != 0 || admissiblePeak(peak, before) {
		t.Fatalf("sample maximum misrepresented: %+v", peak)
	}
	if _, err := os.Stat(peakPath(o, before.EngineID)); !os.IsNotExist(err) {
		t.Fatal("sampling created a bound record")
	}
}
