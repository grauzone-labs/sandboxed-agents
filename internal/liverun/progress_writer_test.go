package liverun

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

// TestProgressWriterContinuesAfterPersistenceFailure proves the output pipe remains drainable when supplemental publication fails. It exercises the output writer and private artifact boundary seam.
func TestProgressWriterContinuesAfterPersistenceFailure(t *testing.T) {
	var log bytes.Buffer
	writer := &progressWriter{destination: &log, filename: filepath.Join(t.TempDir(), "missing", "progress.json")}
	data := []byte("live operation image build: started\n")
	for i := 0; i < 3; i++ {
		if n, err := writer.Write(data); err != nil || n != len(data) {
			t.Fatalf("supplemental failure reached suite pipe: %d %v", n, err)
		}
	}
	if log.Len() != 3*len(data) {
		t.Fatal("private output stopped draining")
	}
}

// failingLog emulates a private log filesystem rejecting writes after disk exhaustion.
type failingLog struct{}

// Write fails every private write without retaining the raw diagnostic publicly.
func (failingLog) Write([]byte) (int, error) { return 0, errors.New("ENOSPC /secret/sentinel") }

// TestProgressWriterDrainsFailedPrivateLog proves log loss remains explicitly unverified while output still drains. It exercises the output writer and private artifact boundary seam.
func TestProgressWriterDrainsFailedPrivateLog(t *testing.T) {
	writer := &progressWriter{destination: failingLog{}, filename: filepath.Join(t.TempDir(), "progress.json")}
	data := []byte("private output\n")
	if n, err := writer.Write(data); err != nil || n != len(data) {
		t.Fatalf("private failure blocked output: %d %v", n, err)
	}
	// Artifact existence alone cannot certify the private log; the completion path reads this fact.
	if !writer.logFailed {
		t.Fatal("lost private log reported verified")
	}
}

// TestProgressIgnoresEmbeddedCommandPayload proves the output-writer status seam accepts only leading host command markers.
func TestProgressIgnoresEmbeddedCommandPayload(t *testing.T) {
	var log bytes.Buffer
	writer := &progressWriter{destination: &log, filename: filepath.Join(t.TempDir(), "progress.json")}
	lines := []string{"live operation Tool probe: started\n", `test.go:1: live output: "live operation rebuild: started\n"` + "\n", `test.go:1: terminal diagnostic "sandboxed-agents build private"` + "\n"}
	for _, line := range lines {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if writer.state.Operation != "Tool probe" {
		t.Fatalf("payload changed status operation: %s", writer.state.Operation)
	}
}
