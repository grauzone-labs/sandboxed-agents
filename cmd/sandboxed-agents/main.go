package main

import (
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// main dispatches CLI arguments and exits with the resulting status.
func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
