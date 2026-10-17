package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

func (s sshSetup) includeLine(expandTokens bool) string {
	path := literalSSHPath(s.config)
	if expandTokens {
		path = tokenExpandedSSHPath(s.config)
	}
	return "Include " + path + "\n"
}

func (s sshSetup) clientIncludeLine() (string, error) {
	if !strings.Contains(s.config, "%") {
		return s.includeLine(false), nil
	}
	output, err := exec.Command("ssh", "-V").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("SSH client version query: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version, ok := strings.CutPrefix(strings.TrimSpace(string(output)), "OpenSSH_")
	version = strings.TrimPrefix(version, "for_Windows_")
	var major, minor int
	if n, _ := fmt.Sscanf(version, "%d.%d", &major, &minor); !ok || n != 2 || major < 1 || minor < 0 {
		return "", fmt.Errorf("cannot determine SSH Include syntax from client version %q", strings.TrimSpace(string(output)))
	}
	// OpenSSH 9.9 added percent expansion to Include; older clients treat
	// both percent signs in %% literally and would miss the managed file.
	return s.includeLine(major > 9 || major == 9 && minor >= 9), nil
}

func (s sshSetup) updateConfiguration(line string) error {
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
	if !hasInclude && line == "" {
		line, err = s.clientIncludeLine()
		if err != nil {
			return err
		}
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
	return writeSSHFile(s.userConfig, append([]byte(line), user...), 0600, true)
}
