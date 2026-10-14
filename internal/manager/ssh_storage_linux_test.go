package manager

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSSHAuthorizationAtomicallyReplacesRootFile proves manager authorization replaces an existing file with one key and mode 0644 while failed ownership preserves previous bytes through the command and filesystem ownership seam.
func TestSSHAuthorizationAtomicallyReplacesRootFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte("old key\nsecond old key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var stdout, stderr bytes.Buffer
	ownershipCalls := 0
	effects := Effects{Identity: func() Identity { return rootIdentity }, ReplaceAuthorizedKey: func(data []byte) error {
		return writeAuthorizedKey(dir, data, func(file *os.File) error {
			ownershipCalls++
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("temporary file=%v %v", info, err)
			}
			return nil
		})
	}}
	if status := RunWithEffects([]string{"ssh-authorize", key}, &stdout, &stderr, effects); status != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != key+"\n" {
		t.Fatalf("authorization=%q error=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 || ownershipCalls != 1 {
		t.Fatalf("file=%v error=%v ownership=%d", info, err, ownershipCalls)
	}
	effects.ReplaceAuthorizedKey = func(data []byte) error {
		return writeAuthorizedKey(dir, data, func(*os.File) error { return errors.New("root ownership refused") })
	}
	if status := RunWithEffects([]string{"ssh-authorize", key}, &stdout, &stderr, effects); status == 0 {
		t.Fatal("ownership error succeeded")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(data, after) {
		t.Fatalf("failed replacement changed bytes=%q %v", after, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "authorized_keys" {
		t.Fatalf("temporary leftovers=%v %v", entries, err)
	}
	effects.RemoveAuthorizedKey = func() error { return removeAuthorizedKey(dir) }
	for range 2 {
		if status := RunWithEffects([]string{"ssh-deauthorize"}, &stdout, &stderr, effects); status != 0 {
			t.Fatalf("deauthorize=%d %q", status, stderr.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorization retained=%v", err)
	}
}
