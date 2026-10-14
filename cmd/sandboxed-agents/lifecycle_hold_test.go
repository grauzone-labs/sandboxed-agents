package main_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// holdLifecycleLock takes the lifecycle lock for container under stateRoot at the path acquireLifecycleLock uses, and releases it when the test ends.
func holdLifecycleLock(t *testing.T, stateRoot, container string) {
	t.Helper()
	dir := filepath.Join(stateRoot, "sandboxed-agents", "group-default", "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(container))
	file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x", hash)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if err := lockFileExclusive(file); err != nil {
		t.Fatalf("hold lifecycle lock: %v", err)
	}
}
