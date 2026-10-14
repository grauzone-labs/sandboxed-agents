//go:build !linux

package manager

import "errors"

// lockManager rejects mutation locking outside the manager Linux runtime.
func lockManager(string) (func(), error) { return nil, errors.New("manager lock requires Linux") }
