package main

import "fmt"

// main prints a fixed preview version and asset hash for stable release gate tests.
func main() {
	fmt.Println("sandboxed-agents 1.0.0-preview.20261012.1\nasset-sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
}
