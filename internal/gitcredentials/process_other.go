//go:build !linux

package gitcredentials

import (
	"errors"
	"os/exec"
	"time"
)

// runCommand refuses execution on platforms where the in-image manager never runs.
func runCommand(cmd *exec.Cmd, deadline time.Time) error {
	return errors.New("Git credential store runs only in the Linux sandbox")
}
