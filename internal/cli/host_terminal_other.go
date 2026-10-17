//go:build !linux && !windows

package cli

import "os"

// isTerminal reports no terminal support on unsupported host platforms.
func isTerminal(*os.File) bool { return false }
