package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCIChecksSkipDocumentationPR proves a documentation-only PR can skip expensive checks through the actual CI script and a temporary Git history.
func TestCIChecksSkipDocumentationPR(t *testing.T) {
	checkout := ciRepository(t)
	base := gitOutput(t, checkout, "rev-parse", "HEAD")
	ciWrite(t, checkout, "docs/guide.md", "new guide\n")
	ciWrite(t, checkout, ".agents/skills/example/SKILL.md", "skill instructions\n")
	ciWrite(t, checkout, "AGENTS.md", "repository instructions\n")
	head := ciCommit(t, checkout)
	assertCIChecks(t, checkout, "pull_request", base, head, false)
}

// TestCIChecksRequireDocumentationConsistency proves changes, deletion, and renaming of executable documentation retain its offline consistency checks through the CI script's real Git diff.
func TestCIChecksRequireDocumentationConsistency(t *testing.T) {
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
				assertCIChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
			})
		}
	}
}

// TestCIChecksRequireFullValidation proves manual, main-push, and uncertain PR comparisons retain full checks through the CI script's event and Git inputs.
func TestCIChecksRequireFullValidation(t *testing.T) {
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
			assertCIChecks(t, checkout, test.event, test.base, test.head, true)
		})
	}
}

// TestCIChecksRetainBuildCoverage proves mixed, unknown, deleted, and renamed build inputs cannot bypass checks through the CI script's real Git diff.
func TestCIChecksRetainBuildCoverage(t *testing.T) {
	for _, path := range []string{
		"main.go", "docs/example.go", "image/context/README.md", "scripts/testdata/sample.md",
		".github/workflows/ci.yml", "npm/README.md", "go.mod", "README.md", "unknown.txt",
	} {
		t.Run(path, func(t *testing.T) {
			checkout := ciRepository(t)
			base := gitOutput(t, checkout, "rev-parse", "HEAD")
			ciWrite(t, checkout, "docs/guide.md", "changed documentation\n")
			ciWrite(t, checkout, path, "changed build input\n")
			assertCIChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
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
			assertCIChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
		})
	}
}

// TestCIChecksCompareEntirePR proves all PR commits count while unrelated base-branch changes do not, through the CI script's merge-base comparison.
func TestCIChecksCompareEntirePR(t *testing.T) {
	t.Run("earlier code change", func(t *testing.T) {
		checkout := ciRepository(t)
		base := gitOutput(t, checkout, "rev-parse", "HEAD")
		ciWrite(t, checkout, "main.go", "package main\n// changed\n")
		ciCommit(t, checkout)
		ciWrite(t, checkout, "docs/guide.md", "later documentation\n")
		assertCIChecks(t, checkout, "pull_request", base, ciCommit(t, checkout), true)
	})
	t.Run("base advances independently", func(t *testing.T) {
		checkout := ciRepository(t)
		ancestor := gitOutput(t, checkout, "rev-parse", "HEAD")
		ciWrite(t, checkout, "docs/guide.md", "PR documentation\n")
		head := ciCommit(t, checkout)
		runGit(t, checkout, "checkout", "--quiet", "--detach", ancestor)
		ciWrite(t, checkout, "main.go", "package main\n// unrelated base change\n")
		base := ciCommit(t, checkout)
		assertCIChecks(t, checkout, "pull_request", base, head, false)
	})
}

// ciRepository creates an isolated temporary Git fixture with a test identity and
// committed build and documentation inputs, skipping tests outside Linux.
func ciRepository(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("CI change classification runs on the Linux job")
	}
	checkout := t.TempDir()
	runGit(t, checkout, "init", "--quiet")
	runGit(t, checkout, "config", "user.name", "CI Test")
	runGit(t, checkout, "config", "user.email", "ci-test@example.invalid")
	ciWrite(t, checkout, "main.go", "package main\n")
	ciWrite(t, checkout, "docs/guide.md", "original guide\n")
	ciCommit(t, checkout)
	return checkout
}

// ciWrite writes a private fixture file at a checkout-relative path, creating
// private parent directories as needed and failing the test on any error.
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

// assertCIChecks runs the real CI classification script in the fixture with the
// event, base, and head inputs and checks its exact Actions-compatible decision
// output, reporting command errors and stderr on failure.
func assertCIChecks(t *testing.T, checkout, event, base, head string, want bool) {
	t.Helper()
	script := filepath.Join(repositoryRoot(t), "scripts", "ci-checks-required.sh")
	command := exec.Command("bash", script)
	command.Dir = checkout
	command.Env = append(os.Environ(), "GITHUB_EVENT_NAME="+event, "CI_BASE_SHA="+base, "CI_HEAD_SHA="+head)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	expected := "run_checks=false\n"
	if want {
		expected = "run_checks=true\n"
	}
	if err != nil || string(output) != expected {
		t.Fatalf("CI decision = %q, error=%v, stderr=%s; want %q", output, err, stderr.String(), expected)
	}
}
