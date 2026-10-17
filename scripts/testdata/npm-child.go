// Command npm-child is the executable boundary fixture for npm launcher tests.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// main reports forwarded arguments and launcher paths or exercises npm child stream and exit-status forwarding.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "forward" {
		data, _ := io.ReadAll(os.Stdin)
		fmt.Printf("stdin:%s", data)
		fmt.Fprintln(os.Stderr, "child stderr")
		os.Exit(23)
	}
	json.NewEncoder(os.Stdout).Encode(struct {
		Args      []string
		Launchers string
	}{os.Args[1:], os.Getenv("SANDBOXED_AGENTS_NPM_LAUNCHERS")})
}
