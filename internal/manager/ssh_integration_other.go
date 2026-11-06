//go:build !linux

package manager

import "os"

// agentFileOwned refuses file storage outside the manager's supported Linux platform.
func agentFileOwned(os.FileInfo) bool { return false }
