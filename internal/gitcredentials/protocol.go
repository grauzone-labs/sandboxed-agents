package gitcredentials

import (
	"errors"
	"strings"
)

// StoreState is the manager's nonsecret report of managed credential store readiness.
type StoreState string

const (
	// StoreNew means neither managed key nor store exists.
	StoreNew StoreState = "new"
	// StoreReady means the managed key and store are structurally valid.
	StoreReady StoreState = "ready"
)

// ParseStoreState validates one readiness response at the host/manager boundary.
func ParseStoreState(value string) (StoreState, error) {
	state := StoreState(strings.TrimSpace(value))
	if state != StoreNew && state != StoreReady {
		return "", errors.New("invalid Git credential store readiness response")
	}
	return state, nil
}

// SetupInput transports terminal passphrases only over the private manager stdin pipe.
type SetupInput struct {
	Passphrase   string
	Confirmation string
}
