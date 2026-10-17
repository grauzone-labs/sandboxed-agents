package main

import (
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// main dispatches manager arguments and exits with the resulting status.
func main() {
	os.Exit(manager.Run(os.Args[1:], os.Stdout, os.Stderr))
}
