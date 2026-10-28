//go:build !linux

package manager

import "os"

// sshFileOwned refuses key storage outside the manager's supported Linux platform.
func sshFileOwned(os.FileInfo) bool { return false }
