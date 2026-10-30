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
	hash := sha256.Sum256([]byte(container))
	holdGroupLock(t, stateRoot, "default", fmt.Sprintf("%x", hash))
}

// holdSSHConfigLock takes the SSH configuration lock of controller group under stateRoot at the path the SSH setup commands lock, and releases it when the test ends.
func holdSSHConfigLock(t *testing.T, stateRoot, group string) {
	t.Helper()
	holdGroupLock(t, stateRoot, group, "ssh-config")
}

// holdGroupLock creates the lock file name in the locks directory of controller group under stateRoot, takes its exclusive lock, and releases it when the test ends.
func holdGroupLock(t *testing.T, stateRoot, group, name string) {
	t.Helper()
	dir := filepath.Join(stateRoot, "sandboxed-agents", "group-"+group, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if err := lockFileExclusive(file); err != nil {
		t.Fatalf("hold %s lock: %v", name, err)
	}
}
