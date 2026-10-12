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
func runGitHubGate(t *testing.T, directory string, extraArgs ...string) ([]byte, error) {
	t.Helper()
	args := append([]string{"run", ".", "--tag", "v1.0.0", "--repository", "owner/repo", "--commit", "0123456789abcdef0123456789abcdef01234567", "--asset-hash", strings.Repeat("a", 64)}, extraArgs...)
	command := exec.Command("go", args...)
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

// TestStableTagWorkflowRunsGate proves workflow calls and v* tag pushes run only the read-only gate through .github/workflows/stable-release-gate.yml's trigger and job mappings.
func TestStableTagWorkflowRunsGate(t *testing.T) {
	text := stableGateWorkflow(t)
	// Inspect only the root event mapping: an unrelated workflow_call string
	// in a comment or job must not make the reusable-workflow check pass.
	_, afterOn, found := strings.Cut(text, "'on':\n")
	if !found {
		t.Fatal("stable-release-gate.yml has no event mapping")
	}
	var eventLines []string
	for _, line := range strings.Split(afterOn, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		eventLines = append(eventLines, line)
	}
	for _, required := range []string{"  workflow_call:", "    inputs:", "      tag:", "        required: true", "        type: string", "  push:", "    tags: ['v*']"} {
		found := false
		for _, line := range eventLines {
			if line == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("stable-release-gate.yml event mapping lacks %q", required)
		}
	}
	for _, required := range []string{"tags: ['v*']", `github.event_name == 'push' || contains(toJSON(inputs), '"tag"')`, "contents: read", "actions: read", "go run ./internal/release/cmd/stable-gate", `STABLE_TAG: ${{ !contains(toJSON(inputs), '"tag"') && github.ref_name || inputs.tag }}`, "--tag \"$STABLE_TAG\"", "bash scripts/release.sh \"${STABLE_TAG#v}\"", "--commit \"$GITHUB_SHA\"", "--asset-hash \"$asset_hash\""} {
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

// TestStableGateCommandFindsValidatedPreview proves an incomplete or failing same-commit candidate cannot hide a validated preview through the real command with fake GitHub evidence.
func TestStableGateCommandFindsValidatedPreview(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	for _, first := range []string{"missing validation file", "failed validation record"} {
		t.Run(first, func(t *testing.T) {
			directory := githubFixture(t)
			var previews []map[string]any
			readJSON(t, directory, "releases.json", &previews)
			candidate := map[string]any{"tag_name": "v1.0.0-preview.20261012.2", "prerelease": true, "draft": false, "assets": []any{}}
			if first == "failed validation record" {
				candidate["assets"] = []any{map[string]any{"id": 5, "name": "live-suite-linux.json"}, map[string]any{"id": 2, "name": "live-suite-windows-11.json"}, map[string]any{"id": 3, "name": "manual-checklist.json"}, map[string]any{"id": 4, "name": "sandboxed-agents-linux-amd64"}}
			}
			previews = append([]map[string]any{candidate}, previews...)
			writeJSON(t, directory, "releases.json", previews)
			var responses map[string]string
			readJSON(t, directory, "responses.json", &responses)
			responses["repos/owner/repo/commits/v1.0.0-preview.20261012.2"] = "commit.json"
			responses["repos/owner/repo/releases/assets/5"] = "failed-linux.json"
			writeJSON(t, directory, "responses.json", responses)
			var record map[string]any
			readJSON(t, directory, "linux.json", &record)
			record["result"] = "fail"
			writeJSON(t, directory, "failed-linux.json", record)
			output, err := runGitHubGate(t, directory)
			if err != nil || !strings.Contains(string(output), "gates passed") {
				t.Fatalf("error=%v output=%s; want later validated preview to pass", err, output)
			}
		})
	}
}

func readJSON(t *testing.T, directory, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

// TestStableGateCommandRefusesLocalEvidence proves a fabricated local pass cannot bypass GitHub collection through the real command's options.
func TestStableGateCommandRefusesLocalEvidence(t *testing.T) {
	directory := githubFixture(t)
	commit := "0123456789abcdef0123456789abcdef01234567"
	records := map[string][]byte{}
	for _, platform := range []string{"linux", "windows-11"} {
		records["live-suite-"+platform+".json"] = []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + commit + `","platform":"` + platform + `","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[]}`)
	}
	records["manual-checklist.json"] = []byte(`{"schema_version":1,"kind":"manual-checklist","commit":"` + commit + `","result":"pass","checks":[{"name":"agent-login","result":"pass","date":"2026-10-12","platform":"linux"}]}`)
	writeJSON(t, directory, "evidence.json", map[string]any{"commit": commit, "ci_commit": commit, "ci_green": true, "asset_hash": strings.Repeat("a", 64), "preview": map[string]any{"commit": commit, "asset_hash": strings.Repeat("a", 64), "records": records}})
	output, err := runGitHubGate(t, directory, "--evidence", filepath.Join(directory, "evidence.json"))
	if err == nil || !strings.Contains(string(output), "flag provided but not defined: -evidence") {
		t.Fatalf("error=%v output=%s; want removed local-evidence option refused", err, output)
	}
}

// TestStableGateCommandNamesInvalidValidationFiles proves ambiguous, empty and platform-mismatched validation records fail with exact prerelease filenames through the real command with fake GitHub evidence.
func TestStableGateCommandNamesInvalidValidationFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	directory := githubFixture(t)
	for _, test := range []struct{ name, file, filename, reason string }{
		{"duplicate result", "linux.json", "live-suite-linux.json", "duplicate"},
		{"empty manual checklist", "manual.json", "manual-checklist.json", "no checks"},
		{"Windows record names Linux", "windows.json", "live-suite-windows-11.json", "platform"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.file)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer os.WriteFile(path, original, 0600)
			if test.name == "duplicate result" {
				if err := os.WriteFile(path, append([]byte(`{"result":"fail",`), original[1:]...), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				var record map[string]any
				readJSON(t, directory, test.file, &record)
				if test.name == "empty manual checklist" {
					record["checks"] = []any{}
				} else {
					record["platform"] = "linux"
				}
				writeJSON(t, directory, test.file, record)
			}
			output, err := runGitHubGate(t, directory)
			if err == nil || !strings.Contains(string(output), test.filename) || !strings.Contains(string(output), test.reason) {
				t.Fatalf("error=%v output=%s; want named %s refusal", err, output, test.filename)
			}
		})
	}
}

// TestStableGateCommandRequiresMatchingPreviewVersion proves wrong-version same-commit previews are skipped with a named version refusal unless a matching validated candidate exists through the real command with fake GitHub evidence.
func TestStableGateCommandRequiresMatchingPreviewVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	for _, scenario := range []string{"only wrong version", "wrong version before matching validated", "wrong version before matching invalid"} {
		t.Run(scenario, func(t *testing.T) {
			directory := githubFixture(t)
			var previews []map[string]any
			readJSON(t, directory, "releases.json", &previews)
			wrong := map[string]any{"tag_name": "v2.0.0-preview.20261012.1", "prerelease": true, "draft": false, "assets": []any{}}
			if scenario == "only wrong version" {
				previews = nil
			}
			if scenario == "wrong version before matching invalid" {
				previews[0]["assets"] = []any{}
			}
			writeJSON(t, directory, "releases.json", append([]map[string]any{wrong}, previews...))
			var responses map[string]string
			readJSON(t, directory, "responses.json", &responses)
			responses["repos/owner/repo/commits/v2.0.0-preview.20261012.1"] = "commit.json"
			writeJSON(t, directory, "responses.json", responses)
			output, err := runGitHubGate(t, directory)
			if scenario == "wrong version before matching validated" {
				if err != nil || !strings.Contains(string(output), "gates passed") {
					t.Fatalf("error=%v output=%s; want matching validated preview pass", err, output)
				}
			} else {
				reason := "version"
				if scenario == "wrong version before matching invalid" {
					reason = "live-suite-linux.json"
				}
				if err == nil || !strings.Contains(string(output), reason) {
					t.Fatalf("error=%v output=%s; want named %s refusal", err, output, reason)
				}
			}
		})
	}
}

// TestStableTagWorkflowVerifiesTagCommit proves the peeled actual tag matches the workflow commit after checkout and before builds or gate execution through .github/workflows/stable-release-gate.yml's step ordering.
func TestStableTagWorkflowVerifiesTagCommit(t *testing.T) {
	text := stableGateWorkflow(t)
	checkout := strings.Index(text, "uses: actions/checkout@v7")
	lookup := strings.Index(text, `git rev-parse --verify "refs/tags/${STABLE_TAG}^{commit}"`)
	compare := strings.Index(text, `[[ "$tag_commit" == "$GITHUB_SHA" ]]`)
	build := strings.Index(text, "bash scripts/release.sh")
	gate := strings.Index(text, "go run ./internal/release/cmd/stable-gate")
	if lookup < 0 || compare < 0 {
		t.Fatal("stable-release-gate.yml does not verify the actual tag commit against GITHUB_SHA")
	}
	if checkout < 0 || build < 0 || gate < 0 || !(checkout < lookup && lookup < compare && compare < build && build < gate) {
		t.Fatal("stable-release-gate.yml must order checkout, actual tag lookup/comparison, build, then gate")
	}
	for _, reason := range []string{"stable release gate: stable tag does not resolve to a commit", "stable release gate: stable tag commit differs from workflow commit"} {
		position := strings.Index(text, reason)
		if position < lookup || position > build {
			t.Fatalf("actual tag check lacks pre-build refusal %q", reason)
		}
	}
}

func stableGateWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../../.github/workflows/stable-release-gate.yml")
	if err != nil {
		t.Fatalf("read stable-release-gate.yml: %v", err)
	}
	return string(data)
}

// TestStableGateCommandContinuesAfterTagLookupFailure proves a failed preview commit lookup cannot hide a later valid preview and remains named when none validates through the real command with fake GitHub evidence.
func TestStableGateCommandContinuesAfterTagLookupFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	for _, scenario := range []string{"later validated candidate", "lookup error only", "later invalid candidate"} {
		t.Run(scenario, func(t *testing.T) {
			directory := githubFixture(t)
			var previews []map[string]any
			readJSON(t, directory, "releases.json", &previews)
			unresolved := map[string]any{"tag_name": "v1.0.0-preview.20261012.2", "prerelease": true, "draft": false, "assets": []any{}}
			if scenario == "lookup error only" {
				previews = nil
			}
			if scenario == "later invalid candidate" {
				previews[0]["assets"] = []any{}
			}
			// No commit response is registered for .2, so the real fake gh exits non-zero.
			writeJSON(t, directory, "releases.json", append([]map[string]any{unresolved}, previews...))
			output, err := runGitHubGate(t, directory)
			if scenario == "later validated candidate" {
				if err != nil || !strings.Contains(string(output), "gates passed") {
					t.Fatalf("error=%v output=%s; want later validated candidate pass", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), "prerelease tag v1.0.0-preview.20261012.2") || !strings.Contains(string(output), "lookup failed") {
				t.Fatalf("error=%v output=%s; want named preview tag lookup refusal", err, output)
			}
		})
	}
}

// TestStableTagWorkflowValidatesCalls proves invalid reusable inputs fail and only well-formed direct preview pushes skip gate steps through .github/workflows/stable-release-gate.yml's event policy and actual verification script.
func TestStableTagWorkflowValidatesCalls(t *testing.T) {
	text := stableGateWorkflow(t)
	if !strings.Contains(text, `if: github.event_name == 'push' || contains(toJSON(inputs), '"tag"')`) {
		t.Fatal("gate job must admit reusable calls independently of their input tag")
	}
	for _, required := range []string{
		`IS_REUSABLE_CALL: ${{ contains(toJSON(inputs), '"tag"') }}`,
		`STABLE_TAG: ${{ !contains(toJSON(inputs), '"tag"') && github.ref_name || inputs.tag }}`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("reusable invocation policy lacks %q", required)
		}
	}
	if strings.Count(text, "if: steps.tag.outputs.preview != 'true'") != 3 {
		t.Fatal("setup, build and gate steps must only skip classified direct previews")
	}
	if runtime.GOOS != "linux" {
		t.Skip("verification script runs on the Linux release runner")
	}
	_, step, found := strings.Cut(text, "      - name: Verify stable tag commit\n")
	if !found {
		t.Fatal("missing tag verification step")
	}
	step, _, _ = strings.Cut(step, "\n      - ")
	_, script, found := strings.Cut(step, "        run: |\n")
	if !found {
		t.Fatal("missing verification script")
	}
	var lines []string
	for _, line := range strings.Split(script, "\n") {
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	script = strings.Join(lines, "\n")
	directory := t.TempDir()
	workflowGit(t, directory, "init", "--quiet")
	workflowGit(t, directory, "-c", "user.name=Workflow Test", "-c", "user.email=workflow@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--quiet", "-m", "test(release): create workflow fixture")
	commit := strings.TrimSpace(workflowGit(t, directory, "rev-parse", "HEAD"))
	workflowGit(t, directory, "-c", "user.name=Workflow Test", "-c", "user.email=workflow@example.invalid", "-c", "tag.gpgsign=false", "tag", "-a", "v1.0.0", "-m", "workflow fixture")
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
			command := exec.Command("bash", "-c", script)
			command.Dir = directory
			command.Env = append(os.Environ(), "GITHUB_EVENT_NAME="+test.event, "STABLE_TAG="+test.tag, "GITHUB_SHA="+commit, "GITHUB_OUTPUT="+outputFile, "GITHUB_REF_NAME=v1.0.0", "IS_REUSABLE_CALL="+reusable)
			output, err := command.CombinedOutput()
			if test.pass && err != nil {
				t.Fatalf("verification failed: %v\n%s", err, output)
			}
			if !test.pass && (err == nil || !strings.Contains(string(output), "stable release gate: invalid stable tag")) {
				t.Fatalf("error=%v output=%s; want named invalid-tag refusal", err, output)
			}
			marker, readErr := os.ReadFile(outputFile)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if strings.Contains(string(marker), "preview=true") != test.preview {
				t.Fatalf("preview marker=%q; want skip=%v", marker, test.preview)
			}
		})
	}
}

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
