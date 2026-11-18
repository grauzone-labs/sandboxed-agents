//go:build !linux

package cli_test

import (
	"testing"
	"time"
)

// waitSharedLock checks the command remains pending after its last object query on hosts
// without Linux's kernel lock inventory; exclusion assertions also inspect fake Podman calls.
func waitSharedLock(t *testing.T, run *sharedCommand, path string) {
	t.Helper()
	select {
	case err := <-run.done:
		t.Fatalf("CLI exited instead of waiting for %s: %v; output=%s", path, err, run.output.String())
	case <-time.After(200 * time.Millisecond):
	}
}
