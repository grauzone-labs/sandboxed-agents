package manager

import (
	"os"
	"path/filepath"
)

func replaceAuthorizedKey(dir string, data []byte) error {
	return writeAuthorizedKey(dir, data, func(file *os.File) error { return file.Chown(0, 0) })
}

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

func removeAuthorizedKey(dir string) error {
	err := os.Remove(filepath.Join(dir, "authorized_keys"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
