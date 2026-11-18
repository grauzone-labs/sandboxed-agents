package liverun

import (
	"bytes"
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
