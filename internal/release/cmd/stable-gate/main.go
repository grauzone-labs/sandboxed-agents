// Command stable-gate refuses a stable release without a validated preview.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	flags := flag.NewFlagSet("stable-gate", flag.ContinueOnError)
	repository := flags.String("repository", "", "GitHub owner/repository")
	commit := flags.String("commit", "", "full release commit SHA")
	hash := flags.String("asset-hash", "", "asset hash printed by the stable binary")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	evidence, err := collectEvidence(*repository, *commit, *hash)
	if err == nil {
		err = release.EvaluateStableGate(evidence)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "stable release gate: %v\n", err)
		return 1
	}
	fmt.Printf("stable release gates passed on %s\n", evidence.Commit)
	return 0
}
