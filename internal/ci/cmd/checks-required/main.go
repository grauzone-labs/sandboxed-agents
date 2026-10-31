// Command checks-required prints the run_checks step output of the pull-request workflow: true when
// the full checks must run, false when the pull request changed only documentation and skills. It
// reads GITHUB_EVENT_NAME, CI_BASE_SHA, and CI_HEAD_SHA, and compares the commits with Git in the
// current directory.
package main

import (
	"fmt"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/ci"
)

// main prints the decision as an Actions output line and explains it on stderr.
func main() {
	fmt.Printf("run_checks=%t\n", ci.ChecksRequired(".", os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("CI_BASE_SHA"), os.Getenv("CI_HEAD_SHA"), os.Stderr))
}
