//go:build !linux

package liverun

// cleanupOpenFlags adds no open flag where filesystem paths cannot name a FIFO that blocks open, as on Windows.
const cleanupOpenFlags = 0
