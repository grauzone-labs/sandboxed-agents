// This executable exposes the embedded manager bytes at the host executable boundary; it is never shipped.
package main

import (
	"io/fs"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// main writes the embedded manager binary to standard output for executable-boundary tests.
func main() {
	data, err := fs.ReadFile(image.Context(), "base/manager.bin")
	if err != nil {
		panic(err)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		panic(err)
	}
}
