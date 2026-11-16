//go:build !linux

package gitcredentials

import "errors"

// terminateAgent refuses execution on platforms where the in-image manager never runs.
func (s Store) terminateAgent(socket string) error {
	return errors.New("Git credential agent runs only in the Linux sandbox")
}

// rememberAgent refuses execution on platforms where the in-image manager never runs.
func (s Store) rememberAgent(a *agentConnection) error {
	return errors.New("Git credential agent runs only in the Linux sandbox")
}
