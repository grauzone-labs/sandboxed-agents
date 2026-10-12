//go:build !linux

package manager

import (
	"fmt"
	"io"
)

// Run refuses execution outside the manager's Linux container platform.
func Run(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "sandboxed-agents-manager requires Linux")
	return 1
}
