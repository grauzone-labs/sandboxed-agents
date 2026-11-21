package liverun

import (
	"bytes"
	"strings"
	"sync"
)

// synchronizedBuffer owns a process query capture.
type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write appends one captured chunk without interleaving concurrent streams.
func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

// Bytes returns a copy of all captured output.
func (b *synchronizedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

// bytesReader supplies a process input reader.
func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }

// platformName returns the stable evidence suffix for a supported host.
func (s *Service) platformName() string {
	if s.Platform == "windows" {
		return "windows-11"
	}
	return "linux"
}

// collectorOutcome recognizes separately retained case durations when optional operation evidence failed.
func collectorOutcome(data []byte, err error, expected string) (bool, bool) {
	if err == nil {
		return strings.TrimSpace(string(data)) == expected, false
	}
	path, optional := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == expected {
			path = true
		}
		if strings.HasPrefix(line, "case durations retained; optional operation evidence: ") {
			optional = true
		}
	}
	return path && optional, path && optional
}
