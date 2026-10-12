package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestStableGateCommandNamesRefusal proves a missing preview produces non-zero exit and a named gate through the real stable-gate command with local offline evidence.
func TestStableGateCommandNamesRefusal(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "evidence.json")
	data, err := json.Marshal(map[string]any{"commit": "0123456789abcdef0123456789abcdef01234567"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, data, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "run", ".", "--evidence", evidence)
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "prerelease") {
		t.Fatalf("error=%v output=%s; want named preview refusal", err, output)
	}
}

// TestStableGateCommandAcceptsGitHubPreview proves release evidence fetched through fake GitHub APIs passes through the real gate command without network or credentials.
func TestStableGateCommandAcceptsGitHubPreview(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("online adapter executes the Linux preview on the Linux release runner")
	}
	directory := githubFixture(t)
	output, err := runGitHubGate(t, directory)
	if err != nil || !strings.Contains(string(output), "gates passed") {
		t.Fatalf("error=%v output=%s; want gate pass", err, output)
	}
}

func githubFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	buildFixture(t, filepath.Join(directory, "gh"), "testdata/gh.go")
	buildFixture(t, filepath.Join(directory, "preview"), "testdata/preview.go")
	commit := "0123456789abcdef0123456789abcdef01234567"
	responses := map[string]string{
		"repos/owner/repo/releases?per_page=100":             "releases.json",
		"repos/owner/repo/commits/v1.0.0-preview.20261012.1": "commit.json",
		"repos/owner/repo/releases/assets/1":                 "linux.json",
		"repos/owner/repo/releases/assets/2":                 "windows.json",
		"repos/owner/repo/releases/assets/3":                 "manual.json",
		"repos/owner/repo/releases/assets/4":                 "preview",
		"repos/owner/repo/actions/workflows/release-reproducibility.yml/runs?head_sha=" + commit + "&event=push&per_page=100": "runs.json",
		"repos/owner/repo/actions/runs/10/jobs?per_page=100":                                                                  "jobs.json",
	}
	writeJSON(t, directory, "responses.json", responses)
	writeJSON(t, directory, "releases.json", []any{map[string]any{"tag_name": "v1.0.0-preview.20261012.1", "prerelease": true, "draft": false, "assets": []any{map[string]any{"id": 1, "name": "live-suite-linux.json"}, map[string]any{"id": 2, "name": "live-suite-windows-11.json"}, map[string]any{"id": 3, "name": "manual-checklist.json"}, map[string]any{"id": 4, "name": "sandboxed-agents-linux-amd64"}}}})
	writeJSON(t, directory, "commit.json", map[string]any{"sha": commit})
	for _, platform := range []string{"linux", "windows-11"} {
		name := "linux.json"
		if platform == "windows-11" {
			name = "windows.json"
		}
		writeJSON(t, directory, name, map[string]any{"schema_version": 2, "kind": "live-suite", "commit": commit, "platform": platform, "result": "pass", "image_part_selected": true, "image_part_ran": true, "image_coverage_complete": true, "checks": []any{map[string]any{"name": "image-build", "result": "pass"}}})
	}
	writeJSON(t, directory, "manual.json", map[string]any{"schema_version": 1, "kind": "manual-checklist", "commit": commit, "result": "pass", "checks": []any{map[string]any{"name": "agent-login", "result": "pass", "date": "2026-10-12", "platform": "linux"}}})
	writeJSON(t, directory, "runs.json", map[string]any{"workflow_runs": []any{map[string]any{"id": 10, "head_sha": commit, "status": "completed", "conclusion": "success"}}})
	writeJSON(t, directory, "jobs.json", map[string]any{"jobs": []any{map[string]any{"name": "linux", "status": "completed", "conclusion": "success"}, map[string]any{"name": "windows", "status": "completed", "conclusion": "success"}}})
	return directory
}

func buildFixture(t *testing.T, path, source string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", path, source)
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v\n%s", err, output)
	}
}
func writeJSON(t *testing.T, directory, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func runGitHubGate(t *testing.T, directory string) ([]byte, error) {
	t.Helper()
	command := exec.Command("go", "run", ".", "--repository", "owner/repo", "--commit", "0123456789abcdef0123456789abcdef01234567", "--asset-hash", strings.Repeat("a", 64))
	command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "GATE_FIXTURE="+directory, "GH_TOKEN=", "GITHUB_TOKEN=", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	return command.CombinedOutput()
}

// TestStableGateCommandRefusesGitHubEvidenceFailures proves missing previews/files, API errors, wrong commits, and failed CI cannot pass through the real gate command with fake GitHub evidence.
func TestStableGateCommandRefusesGitHubEvidenceFailures(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	directory := githubFixture(t)
	for _, test := range []struct{ name, file, body, reason string }{
		{"no preview", "releases.json", "[]", "prerelease"},
		{"draft preview", "releases.json", `[{"tag_name":"v1.0.0-preview.20261012.1","prerelease":true,"draft":true}]`, "prerelease"},
		{"other commit", "commit.json", `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "prerelease"},
		{"missing filename", "releases.json", `[{"tag_name":"v1.0.0-preview.20261012.1","prerelease":true,"assets":[]}]`, "live-suite-linux.json"},
		{"record failure", "linux.json", `{"schema_version":2,"kind":"live-suite","commit":"0123456789abcdef0123456789abcdef01234567","platform":"linux","result":"fail","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[]}`, "live-suite-linux.json"},
		{"CI failed", "runs.json", `{"workflow_runs":[{"id":10,"head_sha":"0123456789abcdef0123456789abcdef01234567","status":"completed","conclusion":"failure"}]}`, "CI"},
		{"newest CI fails", "runs.json", `{"workflow_runs":[{"id":11,"head_sha":"0123456789abcdef0123456789abcdef01234567","status":"completed","conclusion":"failure"},{"id":10,"head_sha":"0123456789abcdef0123456789abcdef01234567","status":"completed","conclusion":"success"}]}`, "CI"},
		{"Windows CI missing", "jobs.json", `{"jobs":[{"name":"linux","status":"completed","conclusion":"success"}]}`, "CI"},
		{"API unavailable", "responses.json", "{}", "prerelease"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.file)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(path, original, 0600)
			if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := runGitHubGate(t, directory)
			if err == nil || !strings.Contains(string(output), test.reason) {
				t.Fatalf("error=%v output=%s; want %s refusal", err, output, test.reason)
			}
		})
	}
}

// TestStableTagWorkflowRunsGate proves stable tags run the gate in a read-only workflow, providing no publication path before its success.
func TestStableTagWorkflowRunsGate(t *testing.T) {
	data, err := os.ReadFile("../../../../.github/workflows/stable-release-gate.yml")
	if os.IsNotExist(err) {
		t.Fatal("stable tags have no gate workflow")
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"tags: ['v*']", "!contains(github.ref_name, '-')", "contents: read", "actions: read", "go run ./internal/release/cmd/stable-gate", "--commit \"$GITHUB_SHA\"", "--asset-hash \"$asset_hash\""} {
		if !strings.Contains(text, required) {
			t.Fatalf("stable workflow lacks %q", required)
		}
	}
	for _, publish := range []string{"contents: write", "packages: write", "id-token: write", "gh release create", "npm publish", "nuget push"} {
		if strings.Contains(text, publish) {
			t.Fatalf("gate workflow exposes publication %q", publish)
		}
	}
}
