package liverun

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestCleanupTotalRejectsFIFOWithoutBlocking proves the status completion-total seam refuses a cleanup-completion.json
// FIFO that has no writer promptly, instead of blocking in open before the regular-file check can run.
func TestCleanupTotalRejectsFIFOWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cleanup-completion.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := readCleanupTotal(directory, Options{}, exitEvidence{})
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("FIFO admitted as completion evidence")
		}
	case <-time.After(5 * time.Second):
		// Release the blocked reader so the test process can finish.
		if writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			writer.Close()
		}
		t.Fatal("reading a FIFO without a writer blocked")
	}
}
