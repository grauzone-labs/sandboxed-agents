package main_test

import "encoding/json"

// liveEngineFacts represents only allowlisted numeric engine observations.
// Nil means unverified; measured zero is valid for free memory and swap.
type liveEngineFacts struct {
	CPUs            *uint64 `json:"cpus"`
	MemoryBytes     *uint64 `json:"memTotal"`
	FreeMemoryBytes *uint64 `json:"memFree"`
	SwapBytes       *uint64 `json:"swapTotal"`
	FreeSwapBytes   *uint64 `json:"swapFree"`
}

// decodeLiveEngineFacts shares Podman's typed info boundary between preflight
// capacity and failure snapshots, excluding nonpositive CPU and total memory.
func decodeLiveEngineFacts(data []byte) (liveEngineFacts, error) {
	var info struct {
		Host liveEngineFacts `json:"host"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return liveEngineFacts{}, err
	}
	facts := info.Host
	if facts.CPUs != nil && *facts.CPUs == 0 {
		facts.CPUs = nil
	}
	if facts.MemoryBytes != nil && *facts.MemoryBytes == 0 {
		facts.MemoryBytes = nil
	}
	return facts, nil
}
