package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func createSSHDirectory(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("SSH path is not a directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := createSSHDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return secureSSHPath(path, os.ModeDir|0700)
}

func writeSSHFile(path string, data []byte, mode os.FileMode, preserve bool) error {
	previous := ""
	if preserve {
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("SSH config is not a regular file: %s", path)
			}
			mode = info.Mode().Perm()
			previous = path
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	resolved, err := resolvedPath(path)
	if err != nil {
		return err
	}
	if previous != "" {
		previous = resolved
	}
	file, err := os.CreateTemp(filepath.Dir(resolved), ".ssh-config-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if err := secureSSHReplacement(file.Name(), mode, previous); err != nil {
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
	return os.Rename(file.Name(), resolved)
}

func (s sshSetup) includeLine() string { return "Include " + literalSSHPath(s.config) + "\n" }

func (s sshSetup) updateConfiguration() error {
	entries, err := os.ReadDir(s.groupDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var managed bytes.Buffer
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "sandbox-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.groupDir, entry.Name(), "entry"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		managed.Write(data)
	}
	user, err := os.ReadFile(s.userConfig)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line := []byte(s.includeLine())
	var withoutInclude bytes.Buffer
	hasInclude := false
	for _, existing := range bytes.SplitAfter(user, []byte("\n")) {
		if bytes.Equal(bytes.TrimSuffix(existing, []byte("\r\n")), bytes.TrimSuffix(line, []byte("\n"))) || bytes.Equal(existing, line) {
			hasInclude = true
			continue
		}
		withoutInclude.Write(existing)
	}
	if managed.Len() == 0 {
		if err := os.Remove(s.config); err != nil && !os.IsNotExist(err) {
			return err
		}
		if hasInclude {
			return writeSSHFile(s.userConfig, withoutInclude.Bytes(), 0600, true)
		}
		return nil
	}
	if err := writeSSHFile(s.config, managed.Bytes(), 0600, false); err != nil {
		return err
	}
	if hasInclude {
		return nil
	}
	if err := createSSHDirectory(filepath.Dir(s.userConfig)); err != nil {
		return err
	}
	return writeSSHFile(s.userConfig, append(line, user...), 0600, true)
}
