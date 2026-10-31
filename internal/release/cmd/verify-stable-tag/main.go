// Command verify-stable-tag is the first step of the stable release gate workflow. A preview tag
// pushed directly is classified as such, so the workflow skips the gate; every other invocation
// requires a stable tag vX.Y.Z that resolves to the workflow commit.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

// main verifies the stable tag named by the workflow environment and exits with the result.
func main() {
	os.Exit(run(os.Getenv, os.Stderr))
}

// run classifies or verifies the tag named by the Actions environment: STABLE_TAG, GITHUB_SHA,
// GITHUB_EVENT_NAME, IS_REUSABLE_CALL, and GITHUB_OUTPUT. A preview tag of a direct push writes
// preview=true to the step output and passes; otherwise the tag must be vX.Y.Z, must resolve to a
// commit in the current checkout, and that commit must be the workflow commit. It returns the exit
// status and names every refusal on stderr.
func run(getenv func(string) string, stderr io.Writer) int {
	tag := getenv("STABLE_TAG")
	if getenv("IS_REUSABLE_CALL") == "false" && getenv("GITHUB_EVENT_NAME") == "push" && release.PreviewTagVersion(tag) != "" {
		if err := appendOutput(getenv("GITHUB_OUTPUT"), "preview=true\n"); err != nil {
			fmt.Fprintln(stderr, "stable release gate:", err)
			return 1
		}
		return 0
	}
	if _, err := release.StableTagVersion(tag); err != nil {
		fmt.Fprintln(stderr, "stable release gate: invalid stable tag")
		return 1
	}
	lookup := exec.Command("git", "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	lookup.Stderr = stderr
	output, err := lookup.Output()
	if err != nil {
		fmt.Fprintln(stderr, "stable release gate: stable tag does not resolve to a commit")
		return 1
	}
	if strings.TrimSpace(string(output)) != getenv("GITHUB_SHA") {
		fmt.Fprintln(stderr, "stable release gate: stable tag commit differs from workflow commit")
		return 1
	}
	return 0
}

// appendOutput appends a line to the Actions step output file, which the runner names in GITHUB_OUTPUT.
func appendOutput(path, line string) error {
	if path == "" {
		return fmt.Errorf("GITHUB_OUTPUT is required")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
