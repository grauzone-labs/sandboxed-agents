//go:build !linux

package cli_test

import (
	"testing"
	"time"
)

// waitHostLock checks the command remains pending after its last object query on hosts
// without Linux's kernel lock inventory; exclusion assertions also inspect fake Podman calls.
func waitHostLock(t *testing.T, run *concurrentCommand, path string) {
	t.Helper()
	select {
	case err := <-run.done:
		t.Fatalf("CLI exited instead of waiting for %s: %v; output=%s", path, err, run.output.String())
	case <-time.After(200 * time.Millisecond):
	}
}
