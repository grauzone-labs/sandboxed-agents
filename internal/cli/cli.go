// Package cli owns command grammar and the host command entry point.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/checks"
)

// Run returns zero only when the requested operation succeeds. Command bodies
// are deliberately absent; a parsed command cannot silently claim success.
func Run(args []string, stdout, stderr io.Writer) int {
	var request Request
	err := checks.Run(checks.Checks{Usage: func() error {
		group, set := os.LookupEnv("SANDBOXED_AGENTS_GROUP")
		if !set {
			group = "default"
		}
		var err error
		request, err = Parse(args, group)
		return err
	}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "usage: sandboxed-agents COMMAND [ARG...]")
		return 2
	}
	if request.Command == "version" {
		fmt.Fprintln(stdout, "sandboxed-agents development")
		return 0
	}
	fmt.Fprintf(stderr, "%s: not implemented\n", request.Command)
	return 1
}
