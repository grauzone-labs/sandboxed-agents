package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordCommands proves the record subcommands create, add, status, and validate a record
// through the command's own dispatch, refusing a pass without the human-observed flag.
func TestRecordCommands(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manual-checklist.json")
	commit := strings.Repeat("a", 40)
	execute := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		status := run(args, &stdout, &stderr)
		return status, stdout.String(), stderr.String()
	}
	if status, _, stderr := execute("record", "init", file, "--commit", commit); status != 0 {
		t.Fatalf("init: %d %s", status, stderr)
	}
	if status, _, _ := execute("record", "add", file, "--name", "github-login", "--result", "pass", "--date", "2026-10-22", "--platform", "linux"); status == 0 {
		t.Fatal("a pass was recorded without --human-observed")
	}
	if status, stdout, stderr := execute("record", "add", file, "--name", "github-login", "--result", "pass", "--date", "2026-10-22", "--platform", "linux", "--human-observed"); status != 0 || !strings.Contains(stdout, "record result fail") {
		t.Fatalf("add: %d %s %s", status, stdout, stderr)
	}
	if status, stdout, _ := execute("record", "status", file); status != 0 || !strings.Contains(stdout, "recorded 1 of 26 identifiers; 25 not run; 0 failed") {
		t.Fatalf("status: %d %s", status, stdout)
	}
	if status, _, _ := execute("record", "validate", file); status != 0 {
		t.Fatal("a valid record did not validate")
	}
	if status, stdout, _ := execute("record", "ids"); status != 0 || len(strings.Fields(stdout)) != 26 {
		t.Fatalf("ids: %d %s", status, stdout)
	}
	if status, _, _ := execute("nonsense"); status != 2 {
		t.Fatal("an unknown command did not exit 2")
	}
	if status, _, stderr := execute("refusals", "no-terminal", "--run", "missing", "--parent", t.TempDir()); status != 1 || !strings.Contains(stderr, "preflight") {
		t.Fatalf("an item without a verified run: %d %s", status, stderr)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}
