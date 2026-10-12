// Command version-symbol prints the canonical linker target for the release build.
package main

import (
	"fmt"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

func main() {
	symbol, err := release.VersionSymbol(os.DirFS("."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(symbol)
}
