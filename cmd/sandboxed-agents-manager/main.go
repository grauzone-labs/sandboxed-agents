package main

import (
	"os"
	"path/filepath"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// main dispatches manager arguments and exits with the resulting status.
func main() {
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "sandboxed-agents-pinentry" {
		args = []string{"__git-pinentry"}
	}
	os.Exit(manager.Run(args, os.Stdout, os.Stderr))
}
