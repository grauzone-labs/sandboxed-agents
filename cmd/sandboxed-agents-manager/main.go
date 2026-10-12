package main

import (
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

func main() {
	os.Exit(manager.Run(os.Args[1:], os.Stdout, os.Stderr))
}
