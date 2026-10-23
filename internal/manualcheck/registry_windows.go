//go:build windows

package manualcheck

import (
	"golang.org/x/sys/windows/registry"
)

// init binds userPath to the raw user environment value HKCU\Environment\Path.
func init() {
	userPath = readUserPath
}

// readUserPath reads HKCU\Environment\Path without expanding environment names and names its
// registry kind; a missing value reads as an empty string of kind none.
func readUserPath() (value, kind string, err error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", err
	}
	defer key.Close()
	value, valueType, err := key.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return "", "none", nil
	}
	if err != nil {
		return "", "", err
	}
	switch valueType {
	case registry.EXPAND_SZ:
		kind = "ExpandString"
	case registry.SZ:
		kind = "String"
	default:
		kind = "other"
	}
	return value, kind, nil
}
