//go:build !linux

// Command benchmark-ownership measures one supplied image in a fresh rootless Podman store; the
// measurement reads /proc and runs the local Linux engine, so other hosts refuse it.
package main

import (
	"fmt"
	"os"
)

// main refuses to run outside Linux.
func main() {
	fmt.Fprintln(os.Stderr, "benchmark-ownership: requires a non-root Linux user")
	os.Exit(2)
}
