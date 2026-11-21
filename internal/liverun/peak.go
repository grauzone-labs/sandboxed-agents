package liverun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	SourceRevision     string
	SourceStartedAt    time.Time
	SourceEndedAt      time.Time
	SourceComplete     bool
	SourceContext      string
}

// peakPath names the maintainer-admitted conservative bound for this host user and engine.
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
			err = retainObservedHighWater(o, record)
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

// knownSpaceFailure refuses known bounds and matching sampled lower observations independently of any unbounded decision.
func knownSpaceFailure(before Snapshot, records ...PeakRecord) (uint64, bool) {
	for _, disk := range before.Disks {
		if disk.FreeBytes == 0 {
			return 0, true
		}
		for _, record := range records {
			if record.EngineID != before.EngineID {
				continue
			}
			if record.PeakAdditionalBytes > 0 && strings.TrimSpace(record.Basis) != "" && record.PeakAdditionalBytes >= disk.FreeBytes {
				return record.PeakAdditionalBytes, true
			}
			for _, sample := range record.Filesystems {
				if sample.Name == disk.Name && sample.Path == disk.Path && sample.Samples > 0 && sample.SampledGrowthBytes >= disk.FreeBytes {
					return sample.SampledGrowthBytes, true
				}
			}
		}
	}
	return 0, false
}

// retainObservedHighWater atomically preserves per-filesystem maxima and their source facts across interrupted or lower runs.
func retainObservedHighWater(o Options, current PeakRecord) error {
	path := observedPeakPath(o, current.EngineID)
	var previous PeakRecord
	err := readJSON(path, &previous)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && previous.EngineID != current.EngineID {
		return fmt.Errorf("previous observed engine identity unverified")
	}
	values := map[string]PeakFilesystem{}
	key := func(s PeakFilesystem) string { return s.Name + "\x00" + s.Path }
	for _, sample := range previous.Filesystems {
		values[key(sample)] = peakSampleSource(sample, previous)
	}
	changed := err != nil
	for _, sample := range current.Filesystems {
		id := key(sample)
		old, exists := values[id]
		if !exists || sample.SampledGrowthBytes > old.SampledGrowthBytes || sample.SampledGrowthBytes == old.SampledGrowthBytes && current.SamplingComplete && !old.SourceComplete {
			values[id] = peakSampleSource(sample, current)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(values) > 64 {
		return fmt.Errorf("observed filesystem history exceeds bound")
	}
	record := current
	record.Filesystems = nil
	record.SamplingComplete = true
	record.SampleContext = "per-filesystem sampled high water; each source is retained separately; transients remain unobserved"
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sample := values[id]
		record.Filesystems = append(record.Filesystems, sample)
		record.SamplingComplete = record.SamplingComplete && sample.SourceComplete
	}
	return writeJSON(path, record)
}

// peakSampleSource associates an observed maximum with the run that actually measured it.
func peakSampleSource(sample PeakFilesystem, record PeakRecord) PeakFilesystem {
	if sample.SourceRevision == "" {
		sample.SourceRevision = record.Revision
		sample.SourceStartedAt = record.SampleStartedAt
		sample.SourceEndedAt = record.SampleEndedAt
		sample.SourceComplete = record.SamplingComplete
		sample.SourceContext = record.SampleContext
	}
	return sample
}
