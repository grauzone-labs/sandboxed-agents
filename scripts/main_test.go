package scripts_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeGoVariable marks a re-execution of this test binary as the fake go command of the workflow
// build-step test: it records the call in the file named by BUILD_MARKER and exits successfully.
const fakeGoVariable = "SANDBOXED_AGENTS_TEST_FAKE_GO"

// TestMain lets the test binary stand in for the go command when the workflow build-step test puts
// it on PATH under that name, and otherwise runs the tests.
func TestMain(m *testing.M) {
	if os.Getenv(fakeGoVariable) == "1" {
		if err := os.WriteFile(os.Getenv("BUILD_MARKER"), []byte("built"), 0600); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeGoDirectory returns a directory whose go executable is this test binary, so that a shell
// step calling go reaches TestMain's fake instead of the toolchain.
func fakeGoDirectory(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(directory, "go")); err != nil {
		t.Fatal(err)
	}
	return directory
}

// ciRepository creates an isolated temporary Git fixture with a test identity and committed build and
// documentation inputs, skipping tests outside Linux.
func ciRepository(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("CI fixtures run on the Linux job")
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

// ciWrite writes a private fixture file at a checkout-relative path, creating private parent
// directories as needed and failing the test on any error.
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
