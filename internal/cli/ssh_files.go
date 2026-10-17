package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// createSSHDirectory creates missing SSH directories with restricted permissions while
// preserving existing directories.
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

// writeSSHFile atomically replaces an SSH file, optionally preserving the existing file's
// permissions.
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

// includeLine quotes the managed configuration path, escaping literal percent
// signs when the client expands Include tokens.
func (s sshSetup) includeLine(expandTokens bool) string {
	path := literalSSHPath(s.config)
	if expandTokens {
		path = tokenExpandedSSHPath(s.config)
	}
	return "Include " + path + "\n"
}

// updateConfiguration adds includeLine only during installation. An empty line
// requests removal cleanup, preserving any Include the user deleted themselves.
func (s sshSetup) updateConfiguration(includeLine string) error {
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
	var withoutInclude bytes.Buffer
	hasInclude := false
	for _, existing := range bytes.SplitAfter(user, []byte("\n")) {
		text := strings.TrimSuffix(strings.TrimSuffix(string(existing), "\n"), "\r")
		if text == strings.TrimSuffix(s.includeLine(false), "\n") || text == strings.TrimSuffix(s.includeLine(true), "\n") {
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
	if hasInclude || includeLine == "" {
		return nil
	}
	if err := createSSHDirectory(filepath.Dir(s.userConfig)); err != nil {
		return err
	}
	return writeSSHFile(s.userConfig, append([]byte(includeLine), user...), 0600, true)
}
