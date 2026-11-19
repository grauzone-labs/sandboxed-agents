package liverun

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// PeakFilesystem retains sampled free-space facts; SampledGrowthBytes is a lower observation, never an upper bound.
type PeakFilesystem struct {
	Name               string
	Path               string
	StartFreeBytes     uint64
	MinimumFreeBytes   uint64
	SampledGrowthBytes uint64
	Samples            int
}

// PeakComponent is a maintainer-reasoned additional-space allowance for one filesystem and source of growth.
type PeakComponent struct {
	Filesystem string
	Kind       string
	Bytes      uint64
	Basis      string
}

// peakPath names the last recorded run on the same host coordination root and engine.
func peakPath(o Options, engine string) string {
	return filepath.Join(o.LockRoot, engineKey(engine)+"-peak.json")
}

// observedPeakPath separates sampled facts from the maintainer-admitted bound at peakPath.
func observedPeakPath(o Options, engine string) string {
	return filepath.Join(o.LockRoot, engineKey(engine)+"-observed-peak.json")
}

// admissiblePeak preserves the original reasoned host-bound record contract.
// Optional sampled facts constrain a supplied bound; a sampled maximum alone is never minted into one.
func admissiblePeak(p PeakRecord, before Snapshot) bool {
	if p.EngineID != before.EngineID || p.PeakAdditionalBytes == 0 || strings.TrimSpace(p.Basis) == "" {
		return false
	}
	for _, sample := range p.Filesystems {
		if sample.SampledGrowthBytes > p.PeakAdditionalBytes {
			return false
		}
	}
	return true
}

// samplePeak records periodic filesystem minima privately without synthesizing a conservative bound.
// A sample cannot cover transient allocations between reads, concurrent work, or a future run's variation.
func (s *Service) samplePeak(ctx context.Context, o Options, before Snapshot) func() bool {
	p := PeakRecord{EngineID: before.EngineID, Revision: o.Revision, SampleStartedAt: before.TakenAt, SampleIntervalSeconds: 10, SampleContext: "whole-filesystem free bytes during suite; includes concurrent writers; transients between samples unobserved", SamplingComplete: true}
	for _, d := range before.Disks {
		p.Filesystems = append(p.Filesystems, PeakFilesystem{Name: d.Name, Path: d.Path, StartFreeBytes: d.FreeBytes, MinimumFreeBytes: d.FreeBytes, Samples: 1})
	}
	persist := func(final bool) bool {
		record := p
		record.SampleEndedAt = time.Now().UTC()
		record.SamplingComplete = final && p.SamplingComplete
		record.Basis = "sampled peak only; conservative bound missing: images, temporary build/suite/artifact storage, sampling gaps and future variation require reasoned coverage"
		err := writeJSON(filepath.Join(o.RunDir, "peak.json"), record)
		if err == nil {
			err = writeJSON(observedPeakPath(o, before.EngineID), record)
		}
		return err == nil
	}
	_ = persist(false)
	stop, done := make(chan struct{}), make(chan bool, 1)
	observe := func() {
		disks, err := s.disks(ctx, o, before.Target)
		if err != nil || len(disks) != len(p.Filesystems) {
			p.SamplingComplete = false
			return
		}
		for i := range p.Filesystems {
			sample := &p.Filesystems[i]
			if disks[i].Name != sample.Name || disks[i].Path != sample.Path {
				p.SamplingComplete = false
				continue
			}
			sample.Samples++
			sample.MinimumFreeBytes = min(sample.MinimumFreeBytes, disks[i].FreeBytes)
			sample.SampledGrowthBytes = sample.StartFreeBytes - sample.MinimumFreeBytes
		}
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				observe()
				_ = persist(false)
			case <-stop:
				observe()
				done <- persist(true)
				return
			}
		}
	}()
	return func() bool { close(stop); return <-done }
}

// observedPeakExceeds invalidates a supplied bound only when independently recorded matching filesystem facts exceed it.
func observedPeakExceeds(o Options, before Snapshot, bound uint64) bool {
	var observed PeakRecord
	if readJSON(observedPeakPath(o, before.EngineID), &observed) != nil || observed.EngineID != before.EngineID {
		return false
	}
	for _, disk := range before.Disks {
		for _, sample := range observed.Filesystems {
			if sample.Name == disk.Name && sample.Path == disk.Path && sample.Samples > 0 && sample.SampledGrowthBytes > bound {
				return true
			}
		}
	}
	return false
}
