//go:build !linux

package manager

import (
	"errors"
	"os"
)

// lockCodexConfiguration refuses configuration locking outside the manager's Linux runtime.
func lockCodexConfiguration(string, bool) (*os.File, error) {
	return nil, errors.New("Codex configuration lock requires Linux")
}
