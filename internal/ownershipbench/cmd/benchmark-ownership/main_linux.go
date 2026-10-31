// Command benchmark-ownership measures one supplied image in a fresh rootless Podman store and never
// alters the active store; docs/ownership-benchmark.md describes the procedure.
package main

import (
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/ownershipbench"
)

// main runs the benchmark with the command-line arguments and exits with its status.
func main() {
	os.Exit(ownershipbench.Main(os.Args[1:], os.Stdout, os.Stderr))
}
