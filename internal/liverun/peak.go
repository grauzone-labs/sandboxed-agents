package liverun

import (
	"context"
	"math"
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

// admissiblePeak requires sample provenance and explicit component coverage beyond a sampled maximum.
func admissiblePeak(p PeakRecord, before Snapshot) bool {
	if p.EngineID != before.EngineID || p.PeakAdditionalBytes == 0 || strings.TrimSpace(p.Basis) == "" || !revisionLine.MatchString(p.Revision) || !p.SamplingComplete || p.SampleStartedAt.IsZero() || p.SampleEndedAt.Before(p.SampleStartedAt) || p.SampleEndedAt.After(time.Now()) || p.SampleIntervalSeconds <= 0 || p.SampleContext == "" || len(p.Filesystems) != len(before.Disks) {
		return false
	}
	for _, disk := range before.Disks {
		found := false
		var sampledGrowth uint64
		for _, sample := range p.Filesystems {
			if sample.Name == disk.Name && sample.Path == disk.Path && sample.Samples >= 2 && sample.SampledGrowthBytes <= p.PeakAdditionalBytes {
				found = true
				sampledGrowth = sample.SampledGrowthBytes
			}
		}
		if !found {
			return false
		}
		covered := map[string]bool{}
		var total uint64
		for _, c := range p.Components {
			if c.Filesystem != disk.Name {
				continue
			}
			if covered[c.Kind] || strings.TrimSpace(c.Basis) == "" || c.Bytes == 0 || math.MaxUint64-total < c.Bytes {
				return false
			}
			covered[c.Kind] = true
			total += c.Bytes
		}
		for _, kind := range []string{"images", "build-temporary", "suite-and-artifacts", "sampling-gaps-and-variation"} {
			if !covered[kind] {
				return false
			}
		}
		if total > p.PeakAdditionalBytes || total < sampledGrowth {
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
			err = writeJSON(peakPath(o, before.EngineID), record)
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
