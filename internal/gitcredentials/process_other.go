//go:build !linux

package gitcredentials

import (
	"errors"
	"os/exec"
)

// runCommand refuses execution on platforms where the in-image manager never runs.
func runCommand(cmd *exec.Cmd) error {
	return errors.New("Git credential store runs only in the Linux sandbox")
}
