//go:build !linux && !windows

package cli

import "os"

func isTerminal(*os.File) bool { return false }
