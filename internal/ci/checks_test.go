package ci_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/ci"
)

// TestChecksRequiredSkipsDocumentationPR proves a pull request that changes only documentation, skills, and
// AGENTS.md can skip the full checks through the decision's real Git comparison of a temporary history.
func TestChecksRequiredSkipsDocumentationPR(t *testing.T) {
	checkout := ciRepository(t)
	base := gitOutput(t, checkout, "rev-parse", "HEAD")
	ciWrite(t, checkout, "docs/guide.md", "new guide\n")
	ciWrite(t, checkout, ".agents/skills/example/SKILL.md", "skill instructions\n")
	ciWrite(t, checkout, "AGENTS.md", "repository instructions\n")
	head := ciCommit(t, checkout)
	assertChecks(t, checkout, "pull_request", base, head, false)
}

// TestChecksRequiredForExecutableDocumentation proves changes, deletion, and renaming of the pages the offline
// consistency tests read keep the full checks through the decision's real Git diff.
func TestChecksRequiredForExecutableDocumentation(t *testing.T) {
	for _, path := range []string{"docs/command-reference.md", "docs/quick-guide-linux.md", "docs/quick-guide-windows.md"} {
		for _, operation := range []string{"change", "delete", "rename"} {
			t.Run(path+"/"+operation, func(t *testing.T) {
				checkout := ciRepository(t)
				ciWrite(t, checkout, path, "original executable documentation\n")
				base := ciCommit(t, checkout)
				switch operation {
				case "change":
					ciWrite(t, checkout, path, "changed executable documentation\n")
				case "delete":
					runGit(t, checkout, "rm", path)
				case "rename":
					runGit(t, checkout, "mv", path, "docs/renamed.md")
				}
				assertChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
			})
		}
	}
}

// TestChecksRequiredForUncertainComparisons proves manual runs, main pushes, unknown events, and missing, invalid,
// unavailable, or empty comparisons keep the full checks through the decision's event and Git inputs.
func TestChecksRequiredForUncertainComparisons(t *testing.T) {
	checkout := ciRepository(t)
	base := gitOutput(t, checkout, "rev-parse", "HEAD")
	ciWrite(t, checkout, "docs/guide.md", "new guide\n")
	head := ciCommit(t, checkout)
	for _, test := range []struct{ name, event, base, head string }{
		{"main push", "push", base, head},
		{"manual", "workflow_dispatch", base, head},
		{"unknown event", "schedule", base, head},
		{"missing base", "pull_request", "", head},
		{"missing head", "pull_request", base, ""},
		{"zero base", "pull_request", strings.Repeat("0", 40), head},
		{"unavailable base", "pull_request", strings.Repeat("a", 40), head},
		{"empty diff", "pull_request", head, head},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertChecks(t, checkout, test.event, test.base, test.head, true)
		})
	}
}

// TestChecksRequiredForBuildInputs proves mixed, unknown, deleted, and renamed build inputs, including Go files below
// documentation directories, cannot bypass the checks through the decision's real Git diff.
func TestChecksRequiredForBuildInputs(t *testing.T) {
	for _, path := range []string{
		"main.go", "docs/example.go", "image/context/README.md", "scripts/testdata/sample.md",
		".github/workflows/ci.yml", "npm/README.md", "go.mod", "README.md", "unknown.txt",
	} {
		t.Run(path, func(t *testing.T) {
			checkout := ciRepository(t)
			base := gitOutput(t, checkout, "rev-parse", "HEAD")
			ciWrite(t, checkout, "docs/guide.md", "changed documentation\n")
			ciWrite(t, checkout, path, "changed build input\n")
			assertChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
		})
	}
	for _, operation := range []string{"delete", "rename into docs"} {
		t.Run(operation, func(t *testing.T) {
			checkout := ciRepository(t)
			base := gitOutput(t, checkout, "rev-parse", "HEAD")
			if operation == "delete" {
				runGit(t, checkout, "rm", "main.go")
			} else {
				runGit(t, checkout, "mv", "main.go", "docs/retired.md")
			}
			assertChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
		})
	}
}

// TestChecksRequiredComparesEntirePR proves every commit of a pull request counts while unrelated base-branch
// changes do not, through the decision's merge-base comparison.
func TestChecksRequiredComparesEntirePR(t *testing.T) {
	t.Run("earlier code change", func(t *testing.T) {
		checkout := ciRepository(t)
		base := gitOutput(t, checkout, "rev-parse", "HEAD")
		ciWrite(t, checkout, "main.go", "package main\n// changed\n")
		ciCommit(t, checkout)
		ciWrite(t, checkout, "docs/guide.md", "later documentation\n")
		assertChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
	})
	t.Run("base advances independently", func(t *testing.T) {
		checkout := ciRepository(t)
		ancestor := gitOutput(t, checkout, "rev-parse", "HEAD")
		ciWrite(t, checkout, "docs/guide.md", "PR documentation\n")
		head := ciCommit(t, checkout)
		runGit(t, checkout, "checkout", "--quiet", "--detach", ancestor)
		ciWrite(t, checkout, "main.go", "package main\n// unrelated base change\n")
		base := ciCommit(t, checkout)
		assertChecks(t, checkout, "pull_request", base, head, false)
	})
}

// TestChecksRequiredCommandPrintsActionsOutput proves the command prints exactly one run_checks line for the
// workflow's step output through the real command in a temporary history.
func TestChecksRequiredCommandPrintsActionsOutput(t *testing.T) {
	checkout := ciRepository(t)
	base := gitOutput(t, checkout, "rev-parse", "HEAD")
	ciWrite(t, checkout, "docs/guide.md", "new guide\n")
	head := ciCommit(t, checkout)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "checks-required")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./internal/ci/cmd/checks-required")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	command := exec.Command(binary)
	command.Dir = checkout
	command.Env = append(os.Environ(), "GITHUB_EVENT_NAME=pull_request", "CI_BASE_SHA="+base, "CI_HEAD_SHA="+head)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil || string(output) != "run_checks=false\n" || !strings.Contains(stderr.String(), "skipping expensive PR checks") {
		t.Fatalf("output=%q error=%v stderr=%s; want run_checks=false with the explanation", output, err, stderr.String())
	}
}

// ciRepository creates an isolated temporary Git fixture with a test identity and committed build and
// documentation inputs.
func ciRepository(t *testing.T) string {
	t.Helper()
	checkout := t.TempDir()
	runGit(t, checkout, "init", "--quiet")
	runGit(t, checkout, "config", "user.name", "CI Test")
	runGit(t, checkout, "config", "user.email", "ci-test@example.invalid")
	ciWrite(t, checkout, "main.go", "package main\n")
	ciWrite(t, checkout, "docs/guide.md", "original guide\n")
	ciCommit(t, checkout)
	return checkout
}

// ciWrite writes a private fixture file at a checkout-relative path, creating private parent directories.
func ciWrite(t *testing.T, checkout, name, contents string) {
	t.Helper()
	path := filepath.Join(checkout, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

// ciCommit stages and commits all fixture changes and returns the new commit SHA.
func ciCommit(t *testing.T, checkout string) string {
	t.Helper()
	runGit(t, checkout, "add", "--all")
	runGit(t, checkout, "commit", "--quiet", "-m", "test(ci): change fixture")
	return gitOutput(t, checkout, "rev-parse", "HEAD")
}

// runGit executes a Git fixture command and fails the test on error.
func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	gitOutput(t, directory, args...)
}

// gitOutput executes Git in a fixture repository and returns trimmed combined output.
func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// assertChecks runs the decision in the fixture with the event, base, and head inputs and checks it.
func assertChecks(t *testing.T, checkout, event, base, head string, want bool) {
	t.Helper()
	var log bytes.Buffer
	if got := ci.ChecksRequired(checkout, event, base, head, &log); got != want {
		t.Fatalf("checks required = %t, log=%s; want %t", got, log.String(), want)
	}
}
