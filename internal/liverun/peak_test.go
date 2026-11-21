package liverun

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stopSamplerOnCleanup wraps a started sampler's finalizer so it runs at most once and registers it with t.Cleanup,
// so a failed assertion still stops the sampler before the test removes its temporary directory. Its explicit call
// returns the first result.
func stopSamplerOnCleanup(t *testing.T, finish func() bool) func() bool {
	t.Helper()
	once := sync.OnceValue(finish)
	t.Cleanup(func() { once() })
	return once
}

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
	finish := stopSamplerOnCleanup(t, service.samplePeak(context.Background(), o, before))
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

// TestSamplingPreservesPriorUsefulPeak proves the sampler/filesystem artifact seam never erases an earlier high observation at startup, after a lower run, or after failed sampling.
func TestSamplingPreservesPriorUsefulPeak(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			root := t.TempDir()
			o := Options{RunDir: filepath.Join(root, "private"), LockRoot: filepath.Join(root, "locks"), Revision: strings.Repeat("a", 40)}
			for _, path := range []string{o.RunDir, o.LockRoot} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := Snapshot{EngineID: "engine:/storage", Target: Target{Storage: "/storage"}, Disks: []Disk{{Name: "engine", Path: "/storage", FreeBytes: 900}}, TakenAt: time.Now().UTC()}
			previous := PeakRecord{EngineID: before.EngineID, Revision: strings.Repeat("b", 40), SamplingComplete: true, Filesystems: []PeakFilesystem{{Name: "engine", Path: "/storage", StartFreeBytes: 900, MinimumFreeBytes: 500, SampledGrowthBytes: 400, Samples: 2}}}
			if err := writeJSON(observedPeakPath(o, before.EngineID), previous); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(observedPeakPath(o, before.EngineID))
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{Platform: "linux", FreeSpace: func(string) (uint64, error) {
				if failure {
					return 0, fmt.Errorf("fixture query failure")
				}
				return 800, nil
			}}
			finish := stopSamplerOnCleanup(t, service.samplePeak(context.Background(), o, before))
			ongoing, _ := os.ReadFile(observedPeakPath(o, before.EngineID))
			if !bytes.Equal(old, ongoing) {
				t.Fatal("new run startup erased previous evidence")
			}
			if !finish() {
				t.Fatal("private observations not retained")
			}
			var retained PeakRecord
			if err := readJSON(observedPeakPath(o, before.EngineID), &retained); err != nil {
				t.Fatal(err)
			}
			if len(retained.Filesystems) != 1 || retained.Filesystems[0].SampledGrowthBytes != 400 {
				t.Fatalf("useful observation erased: %+v", retained)
			}
		})
	}
}
