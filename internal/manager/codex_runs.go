package manager

import (
	"errors"
	"os"
	"path/filepath"
)

// codexRunRecovery directs explicit recovery when a managed reader may have outlived its manager.
const codexRunRecovery = "interrupted managed Codex run requires recovery; see docs/codex-proxy.md"

// recordCodexRun records admission under the shared Codex configuration lock before the native process starts.
// The caller removes only its own marker after known normal completion, never from a defer.
func recordCodexRun(root string) (string, error) {
	dir := filepath.Join(root, "codex-runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New(codexRunRecovery)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New(codexRunRecovery)
	}
	file, err := os.CreateTemp(dir, "run-")
	if err != nil {
		return "", errors.New(codexRunRecovery)
	}
	if err := file.Close(); err != nil {
		return "", errors.New(codexRunRecovery)
	}
	return file.Name(), nil
}

// checkCodexRunMarkers refuses any retained or unreadable run state under the exclusive Codex configuration lock.
func checkCodexRunMarkers(root string) error {
	dir := filepath.Join(root, "codex-runs")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New(codexRunRecovery)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return errors.New(codexRunRecovery)
	}
	return nil
}
