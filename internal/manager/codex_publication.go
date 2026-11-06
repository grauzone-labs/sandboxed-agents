package manager

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
)

// codexProxyRecoveryDir names the private recovery directory and interruption marker.
const codexProxyRecoveryDir = ".sandboxed-agents-proxy"

// codexRecovery directs manual recovery without exposing paths or errors supplied by callers.
const codexRecovery = "Codex publication requires recovery; see docs/codex-proxy.md for ~/.codex/.sandboxed-agents-proxy"

// checkCodexProxyRecovery refuses existing or uninspectable markers in the Codex home.
func checkCodexProxyRecovery(home string) error {
	return checkCodexRecoveryDirectory(filepath.Join(home, ".codex", codexProxyRecoveryDir))
}

// checkCodexRecoveryDirectory distinguishes existing markers, including dangling symlinks, from inspection failures.
func checkCodexRecoveryDirectory(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect Codex publication recovery state; operation refused")
	}
	return errors.New(codexRecovery)
}

// codexPublicationFile tracks one moved original and its proposed active replacement.
type codexPublicationFile struct {
	active, previous, staged string
	old, next                []byte
	captured, published      bool
	removed                  bool
	mode                     os.FileMode
}

// publishCodexFiles moves auth out before changing destinations and publishes without clobbering.
// The manager lock serializes manager writers; observed direct-writer conflicts require
// manual recovery. This is not a filesystem CAS or a power-loss durability protocol.
func publishCodexFiles(configPath, authPath string, before, oldAuth, after, newAuth []byte, effects Effects) error {
	mkdir := effects.Mkdir
	if mkdir == nil {
		mkdir = os.Mkdir
	}
	rename, link, remove := effects.RenameFile, effects.LinkFile, effects.RemoveFile
	if rename == nil {
		rename = os.Rename
	}
	if link == nil {
		link = os.Link
	}
	if remove == nil {
		remove = os.Remove
	}
	recoveryDir := filepath.Join(filepath.Dir(configPath), codexProxyRecoveryDir)
	recovery := errors.New(codexRecovery)
	if err := checkCodexRecoveryDirectory(recoveryDir); err != nil {
		return err
	}
	if !codexFileMatches(configPath, before) || !codexFileMatches(authPath, oldAuth) {
		return errors.New("Codex files changed during staging; previous configuration and login unchanged")
	}
	if err := mkdir(recoveryDir, 0700); err != nil {
		if os.IsExist(err) {
			return recovery
		}
		return errors.New("cannot prepare Codex publication; previous configuration and login unchanged")
	}
	// No deferred recovery directory removal: an interruption must leave the marker intact.
	config := &codexPublicationFile{active: configPath, previous: filepath.Join(recoveryDir, "config.toml.prev"), old: before, next: after}
	auth := &codexPublicationFile{active: authPath, previous: filepath.Join(recoveryDir, "auth.json.prev"), old: oldAuth, next: newAuth}
	artifacts := map[string][]byte{}
	cleanup := func() (complete, removalFailed bool) {
		// Verify every retained artifact before removing any of them.
		for path, expected := range artifacts {
			if !codexFileMatches(path, expected) {
				return false, false
			}
		}
		for path := range artifacts {
			if remove(path) != nil {
				return false, true
			}
			delete(artifacts, path)
			for _, file := range []*codexPublicationFile{config, auth} {
				if path == file.previous {
					file.removed = true
				}
			}
		}
		if remove(recoveryDir) != nil {
			return false, true
		}
		return true, false
	}
	rollback := func(message string) error {
		// Quarantine auth first, so interrupted rollback cannot pair it with a restored destination.
		known := true
		for _, file := range []*codexPublicationFile{auth, config} {
			if !file.captured && file.old != nil {
				if !codexFileMatches(file.active, file.old) {
					known = false
				}
				// Auth was never captured, so no destination change is safe.
				if file == auth {
					break
				}
				continue
			}
			expected := []byte(nil)
			if file.published {
				expected = file.next
			}
			if _, err := os.Lstat(file.active); os.IsNotExist(err) {
				if file.published {
					known = false
				}
				continue
			} else if err != nil {
				return errors.New("Codex publication failed and configuration restore failed; " + codexRecovery)
			}
			// A fresh private subdirectory makes the failed name unique without replacing anything.
			failedDir, err := os.MkdirTemp(recoveryDir, ".failed-")
			if err != nil {
				return errors.New("Codex publication failed and configuration restore failed; " + codexRecovery)
			}
			failed := filepath.Join(failedDir, filepath.Base(file.active)+".failed")
			if rename(file.active, failed) != nil {
				return errors.New("Codex publication failed and configuration restore failed; " + codexRecovery)
			}
			if expected == nil || !codexFileMatches(failed, expected) {
				known = false
			} else {
				artifacts[failed] = expected
			}
		}
		if !known {
			return recovery
		}
		// Restore the destination first and auth last, always through no-clobber links.
		for _, file := range []*codexPublicationFile{config, auth} {
			if file.captured {
				if !codexFileMatches(file.previous, file.old) || !codexFileMatches(file.active, nil) || link(file.previous, file.active) != nil {
					return errors.New("Codex publication failed and configuration restore failed; " + codexRecovery)
				}
			}
			if !codexFileMatches(file.active, file.old) {
				return recovery
			}
		}
		// Remove only empty quarantine directories after their verified contents are removed.
		for path, expected := range artifacts {
			if !codexFileMatches(path, expected) {
				return recovery
			}
		}
		for path := range artifacts {
			if remove(path) != nil {
				return recovery
			}
			if filepath.Dir(path) != recoveryDir && remove(filepath.Dir(path)) != nil {
				return recovery
			}
		}
		if remove(recoveryDir) != nil {
			return recovery
		}
		return errors.New(message)
	}
	// Stage only new bytes. An unchanged login is later linked from its moved original,
	// retaining its exact bytes and mode without making a credential copy.
	for _, file := range []*codexPublicationFile{config, auth} {
		if file.next == nil || (file == auth && file.old != nil && bytes.Equal(file.old, file.next)) {
			continue
		}
		path, err := stageCodexPublication(recoveryDir, file.next)
		if err != nil {
			if complete, _ := cleanup(); !complete {
				return recovery
			}
			return errors.New("cannot publish Codex configuration; previous configuration and login unchanged")
		}
		file.staged = path
		artifacts[path] = file.next
	}
	if !codexFileMatches(configPath, before) || !codexFileMatches(authPath, oldAuth) {
		if complete, _ := cleanup(); !complete {
			return recovery
		}
		return errors.New("Codex files changed during staging; previous configuration and login unchanged")
	}
	for _, file := range []*codexPublicationFile{auth, config} {
		if file.old == nil {
			if !codexFileMatches(file.active, nil) {
				return recovery
			}
			continue
		}
		if rename(file.active, file.previous) != nil {
			return rollback("cannot publish Codex configuration; previous configuration and login unchanged")
		}
		file.captured = true
		// A raced replacement is preserved in the recovery directory, never discarded or overwritten.
		if !codexFileMatches(file.previous, file.old) {
			return recovery
		}
		info, err := os.Stat(file.previous)
		if err != nil {
			return recovery
		}
		file.mode = info.Mode()
		artifacts[file.previous] = file.old
	}
	if !codexFileMatches(authPath, nil) {
		return rollback("cannot publish Codex configuration")
	}
	for _, file := range []*codexPublicationFile{config, auth} {
		message := "cannot publish Codex configuration; previous configuration and login unchanged"
		if file == auth {
			message = "cannot publish Codex login; previous configuration restored"
		}
		if file.next == nil {
			continue
		}
		source := file.staged
		if source == "" {
			source = file.previous
		}
		if link(source, file.active) != nil {
			return rollback(message)
		}
		file.published = true
		if file.staged != "" {
			if remove(file.staged) != nil {
				return rollback(message)
			}
			delete(artifacts, file.staged)
		}
	}
	if !codexFileMatches(configPath, after) || !codexFileMatches(authPath, newAuth) {
		return rollback("cannot publish Codex login; previous configuration restored")
	}
	if complete, removalFailed := cleanup(); !complete {
		if !removalFailed {
			return recovery
		}
		// Only this invocation knows which originals its cleanup removed. Rebuild
		// those names without replacing anything before the usual safe rollback.
		for _, file := range []*codexPublicationFile{config, auth} {
			if !file.removed {
				continue
			}
			candidate, err := stageCodexPublication(recoveryDir, file.old)
			if err != nil {
				return recovery
			}
			artifacts[candidate] = file.old
			if os.Chmod(candidate, file.mode) != nil || link(candidate, file.previous) != nil {
				return recovery
			}
			artifacts[file.previous] = file.old
			if remove(candidate) != nil {
				return recovery
			}
			delete(artifacts, candidate)
		}
		return rollback("cannot finish Codex publication; previous configuration and login restored")
	}
	return nil
}

// codexFileMatches checks absence or exact regular-file bytes without following symlinks.
func codexFileMatches(path string, expected []byte) bool {
	info, err := os.Lstat(path)
	if expected == nil {
		return os.IsNotExist(err)
	}
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	actual, err := os.ReadFile(path)
	return err == nil && bytes.Equal(actual, expected)
}

// stageCodexPublication writes and syncs a private candidate within the recovery directory.
func stageCodexPublication(recoveryDir string, data []byte) (string, error) {
	file, err := os.CreateTemp(recoveryDir, ".publish-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
