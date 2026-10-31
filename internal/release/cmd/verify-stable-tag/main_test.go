package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyStableTagClassifiesCalls proves direct preview pushes alone write the preview marker and pass, while
// malformed, empty, and reusable-call preview tags are refused by name through the command's environment seam
// against temporary Git data.
func TestVerifyStableTagClassifiesCalls(t *testing.T) {
	directory, commit := stableTagFixture(t)
	for _, test := range []struct {
		name, event, tag      string
		pass, preview, called bool
	}{
		{"direct stable", "push", "v1.0.0", true, false, false},
		{"direct preview", "push", "v1.0.0-preview.20261012.1", true, true, false},
		{"direct invalid hyphen", "push", "v1.0.0-other", false, false, false},
		{"direct malformed preview", "push", "v1.0.0-preview.bad.1", false, false, false},
		{"called stable", "workflow_dispatch", "v1.0.0", true, false, true},
		{"called preview on push", "push", "v1.0.0-preview.20261012.1", false, false, true},
		{"called invalid hyphen", "workflow_dispatch", "v1.0.0-other", false, false, true},
		{"called invalid plain", "push", "invalid", false, false, true},
		{"called empty on stable ref", "push", "", false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputFile := filepath.Join(t.TempDir(), "output")
			reusable := "false"
			if test.called {
				reusable = "true"
			}
			environment := map[string]string{"GITHUB_EVENT_NAME": test.event, "STABLE_TAG": test.tag, "GITHUB_SHA": commit, "GITHUB_OUTPUT": outputFile, "GITHUB_REF_NAME": "v1.0.0", "IS_REUSABLE_CALL": reusable}
			var stderr bytes.Buffer
			status := runIn(t, directory, environment, &stderr)
			if test.pass && status != 0 {
				t.Fatalf("verification failed: status=%d\n%s", status, stderr.String())
			}
			if !test.pass && (status == 0 || !strings.Contains(stderr.String(), "stable release gate: invalid stable tag")) {
				t.Fatalf("status=%d stderr=%s; want named invalid-tag refusal", status, stderr.String())
			}
			marker, err := os.ReadFile(outputFile)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if strings.Contains(string(marker), "preview=true") != test.preview {
				t.Fatalf("preview marker=%q; want %v", marker, test.preview)
			}
		})
	}
}

// TestVerifyStableTagVerifiesRefs proves matching annotated and lightweight tags pass while a missing tag and a tag
// on another commit are refused by name, in that order of checks, through the command against temporary Git data.
func TestVerifyStableTagVerifiesRefs(t *testing.T) {
	directory, commit := stableTagFixture(t)
	for _, test := range []struct{ name, tag, sha, refusal string }{
		{"matching annotated", "v1.0.0", commit, ""},
		{"matching lightweight", "v1.0.1", commit, ""},
		{"missing", "v1.0.3", commit, "stable release gate: stable tag does not resolve to a commit"},
		{"mismatched", "v1.0.2", commit, "stable release gate: stable tag commit differs from workflow commit"},
		{"missing before mismatched", "v1.0.3", strings.Repeat("0", 40), "stable release gate: stable tag does not resolve to a commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			status := runIn(t, directory, map[string]string{"STABLE_TAG": test.tag, "GITHUB_SHA": test.sha, "IS_REUSABLE_CALL": "true", "GITHUB_EVENT_NAME": "push"}, &stderr)
			if test.refusal == "" {
				if status != 0 {
					t.Fatalf("matching tag failed: status=%d\n%s", status, stderr.String())
				}
			} else if status == 0 || !strings.Contains(stderr.String(), test.refusal) {
				t.Fatalf("status=%d stderr=%s; want %q", status, stderr.String(), test.refusal)
			}
		})
	}
}

// TestVerifyStableTagRequiresOutputFileForPreview proves a direct preview push without GITHUB_OUTPUT fails instead of
// passing silently, so the workflow cannot skip the gate on an unrecorded classification.
func TestVerifyStableTagRequiresOutputFileForPreview(t *testing.T) {
	directory, commit := stableTagFixture(t)
	var stderr bytes.Buffer
	status := runIn(t, directory, map[string]string{"STABLE_TAG": "v1.0.0-preview.20261012.1", "GITHUB_SHA": commit, "IS_REUSABLE_CALL": "false", "GITHUB_EVENT_NAME": "push"}, &stderr)
	if status == 0 || !strings.Contains(stderr.String(), "GITHUB_OUTPUT is required") {
		t.Fatalf("status=%d stderr=%s; want refusal naming GITHUB_OUTPUT", status, stderr.String())
	}
}

// runIn runs the command's logic in the fixture directory with the given environment and returns its status.
func runIn(t *testing.T, directory string, environment map[string]string, stderr *bytes.Buffer) int {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	return run(func(key string) string { return environment[key] }, stderr)
}

// workflowGit runs Git in a fixture repository without system or global configuration.
func workflowGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git: %v\n%s", err, output)
	}
	return string(output)
}

// stableTagFixture creates a detached repository with matching annotated and lightweight tags and a later commit.
func stableTagFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	workflowGit(t, directory, "init", "--quiet")
	workflowGit(t, directory, "-c", "user.name=Workflow Test", "-c", "user.email=workflow@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "test(release): create workflow fixture")
	commit := strings.TrimSpace(workflowGit(t, directory, "rev-parse", "HEAD"))
	workflowGit(t, directory, "-c", "user.name=Workflow Test", "-c", "user.email=workflow@example.invalid", "-c", "tag.gpgsign=false", "tag", "-a", "v1.0.0", "-m", "workflow fixture")
	workflowGit(t, directory, "tag", "v1.0.1")
	workflowGit(t, directory, "-c", "user.name=Workflow Test", "-c", "user.email=workflow@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "test(release): advance workflow fixture")
	workflowGit(t, directory, "tag", "v1.0.2")
	workflowGit(t, directory, "checkout", "--quiet", commit)
	return directory, commit
}
