//go:build !linux

package manager

import "errors"

// lockManager rejects mutation locking outside the manager Linux runtime.
func lockManager(string) (func(), error) { return nil, errors.New("manager lock requires Linux") }

// tryLock rejects advisory locking outside the manager Linux runtime.
func tryLock(string) (func(), bool, error) {
	return nil, false, errors.New("manager lock requires Linux")
}

// exclusivelyLocked rejects lock probes outside the manager Linux runtime.
func exclusivelyLocked(string) (bool, error) { return false, errors.New("manager lock requires Linux") }
