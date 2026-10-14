//go:build !linux

package manager

import "errors"

func lockManager(string) (func(), error) { return nil, errors.New("manager lock requires Linux") }
