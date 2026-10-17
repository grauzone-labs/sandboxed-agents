//go:build !windows

package cli

import "os"

// secureSSHPath applies the requested Unix permission bits to an SSH path.
func secureSSHPath(path string, mode os.FileMode) error { return os.Chmod(path, mode.Perm()) }

// secureSSHReplacement applies the requested Unix permission bits to an SSH replacement file.
func secureSSHReplacement(path string, mode os.FileMode, _ string) error {
	return secureSSHPath(path, mode)
}
