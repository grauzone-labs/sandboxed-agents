// Command live-run executes the host phases of an explicitly authorized live invocation.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// main runs one host phase and returns nonzero when its evidence cannot be verified.
func main() {
	if err := liverun.New().RunDispatch(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
