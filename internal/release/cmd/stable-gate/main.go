// Command stable-gate refuses a stable release without a validated preview.
package main

import (
	"flag"
	"fmt"
	"os"
)

// main exits with the stable release gate command status.
func main() { os.Exit(run(os.Args[1:])) }

// run parses gate options, collects GitHub evidence, and reports whether the stable release prerequisites pass.
func run(args []string) int {
	flags := flag.NewFlagSet("stable-gate", flag.ContinueOnError)
	tag := flags.String("tag", "", "stable release tag vX.Y.Z")
	repository := flags.String("repository", "", "GitHub owner/repository")
	commit := flags.String("commit", "", "full release commit SHA")
	hash := flags.String("asset-hash", "", "asset hash printed by the stable binary")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	evidence, err := collectEvidence(*repository, *tag, *commit, *hash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stable release gate: %v\n", err)
		return 1
	}
	fmt.Printf("stable release gates passed on %s\n", evidence.Commit)
	return 0
}
