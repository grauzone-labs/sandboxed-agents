package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func lifecycleStateRoot(platform string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if platform == "windows" {
		root = os.Getenv("LOCALAPPDATA")
		if root == "" {
			return "", fmt.Errorf("LOCALAPPDATA is required for host state")
		}
	} else if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("host state path must be absolute: %s", root)
	}
	return filepath.Join(root, "sandboxed-agents"), nil
}

func acquireLifecycleLock(root string, request Request) (*os.File, error) {
	dir := filepath.Join(root, "group-"+request.Group, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create lifecycle lock directory: %w", err)
	}
	hash := sha256.Sum256([]byte(DerivedNames(request.Group, request.Name).Container))
	path := filepath.Join(dir, fmt.Sprintf("%x", hash))
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("lifecycle lock path is not a regular file: %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lifecycle lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("lifecycle lock path is not a regular file: %s", path)
	}
	if err := lockLifecycleFile(file); err != nil {
		file.Close()
		if lifecycleLockBusy(err) {
			return nil, fmt.Errorf("sandbox %q: another lifecycle command is in progress; retry", request.Name)
		}
		return nil, fmt.Errorf("lock sandbox %q: %w", request.Name, err)
	}
	return file, nil
}
