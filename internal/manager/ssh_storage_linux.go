package manager

import (
	"os"
	"path/filepath"
)

// replaceAuthorizedKey atomically replaces authorized_keys with a root-owned mode-0644 file.
func replaceAuthorizedKey(dir string, data []byte) error {
	return writeAuthorizedKey(dir, data, func(file *os.File) error { return file.Chown(0, 0) })
}

// writeAuthorizedKey applies ownership, permissions, and synced contents before publishing authorized_keys.
func writeAuthorizedKey(dir string, data []byte, own func(*os.File) error) error {
	file, err := os.CreateTemp(dir, ".authorized_keys-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := own(file); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "authorized_keys"))
}

// removeAuthorizedKey deletes authorized_keys and treats an absent file as success.
func removeAuthorizedKey(dir string) error {
	err := os.Remove(filepath.Join(dir, "authorized_keys"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
