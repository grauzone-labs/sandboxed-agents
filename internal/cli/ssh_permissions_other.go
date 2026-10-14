//go:build !windows

package cli

import "os"

func secureSSHPath(path string, mode os.FileMode) error { return os.Chmod(path, mode.Perm()) }
