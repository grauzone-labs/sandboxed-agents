// Package ci decides, for the pull-request workflow, whether a change needs the full checks or only
// touched documentation and skills.
package ci

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// commitPattern matches the full commit SHA the workflow passes for the base and head of a pull request.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// executableDocumentation lists the pages that the offline consistency tests read, so a change to
// them keeps the full checks.
var executableDocumentation = []string{"docs/command-reference.md", "docs/quick-guide-linux.md", "docs/quick-guide-windows.md"}

// ChecksRequired reports whether the full checks must run. Main pushes and manual runs always need
// them. A pull request needs them unless the comparison of its base and head commits, run with Git
// in directory, shows only documentation, skill, or AGENTS.md changes, none of which is a Go file
// or an executable documentation page. Any doubt, such as a missing or unavailable comparison
// commit or an empty diff, keeps the full checks and is explained on log.
func ChecksRequired(directory, event, base, head string, log io.Writer) bool {
	if event != "pull_request" {
		return true
	}
	if !commitPattern.MatchString(base) || !commitPattern.MatchString(head) {
		fmt.Fprintln(log, "CI comparison commits missing or invalid; running all checks.")
		return true
	}
	diff := exec.Command("git", "diff", "--name-only", "--no-renames", "-z", base+"..."+head, "--")
	diff.Dir = directory
	diff.Stderr = log
	output, err := diff.Output()
	if err != nil {
		fmt.Fprintln(log, "CI comparison unavailable; running all checks.")
		return true
	}
	if len(output) == 0 {
		fmt.Fprintln(log, "CI comparison has no changed paths; running all checks.")
		return true
	}
	for _, path := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
		if !documentationOnly(string(path)) {
			return true
		}
	}
	fmt.Fprintln(log, "Only documentation or skill paths changed; skipping expensive PR checks.")
	return false
}

// documentationOnly tells whether a changed path can skip the full checks: a page below docs, a
// skill below .agents, or AGENTS.md, but never a Go file, which can live below those directories
// too, and never an executable documentation page.
func documentationOnly(path string) bool {
	if strings.HasSuffix(path, ".go") {
		return false
	}
	for _, page := range executableDocumentation {
		if path == page {
			return false
		}
	}
	return strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, ".agents/") || path == "AGENTS.md"
}
