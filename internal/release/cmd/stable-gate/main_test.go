package main_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// githubFixture builds fake GitHub and preview executables and writes evidence for a passing stable gate.
func githubFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	buildFixture(t, filepath.Join(directory, "gh"), "testdata/gh.go")
	buildFixture(t, filepath.Join(directory, "preview"), "testdata/preview.go")
	preview, err := os.ReadFile(filepath.Join(directory, "preview"))
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, directory, "attestations.json", map[string]map[string]string{"refs/tags/v1.0.0-preview.20261012.1": {"repository": "owner/repo", "workflow": "owner/repo/.github/workflows/release.yml", "commit": "0123456789abcdef0123456789abcdef01234567", "hosted": "true", "sha256": fmt.Sprintf("%x", sha256.Sum256(preview))}})
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

// buildFixture compiles a test executable without network dependency resolution.
func buildFixture(t *testing.T, path, source string, flags ...string) {
	t.Helper()
	args := append([]string{"build", "-o", path}, flags...)
	command := exec.Command("go", append(args, source)...)
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v\n%s", err, output)
	}
}

// TestStableGateCommandAuthenticatesPreviewBeforeExecution proves missing or mismatched provenance never executes downloaded bytes through the real gate CLI and fake GitHub verifier.
func TestStableGateCommandAuthenticatesPreviewBeforeExecution(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("preview execution requires the Linux release runner")
	}
	for _, field := range []string{"missing", "repository", "workflow", "workflow-prefix", "workflow-metachar", "commit", "ref", "ref-case", "sha256", "hosted", "verifier"} {
		t.Run(field, func(t *testing.T) {
			directory := githubFixture(t)
			marker := filepath.Join(directory, "executed-preview.json")
			buildFixture(t, filepath.Join(directory, "preview"), "testdata/preview.go", "-ldflags", "-X "+strconv.Quote("main.markerPath="+marker))
			binary, err := os.ReadFile(filepath.Join(directory, "preview"))
			if err != nil {
				t.Fatal(err)
			}
			var proofs map[string]map[string]string
			readJSON(t, directory, "attestations.json", &proofs)
			ref := "refs/tags/v1.0.0-preview.20261012.1"
			proofs[ref]["sha256"] = fmt.Sprintf("%x", sha256.Sum256(binary))
			if field == "verifier" {
				if err := os.WriteFile(filepath.Join(directory, "gh-version"), []byte("2.83.2"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if field == "missing" {
				delete(proofs, ref)
			} else if field == "ref" {
				proofs[ref+"-wrong"] = proofs[ref]
				delete(proofs, ref)
			} else if field == "ref-case" {
				proofs[ref]["ref"] = strings.Replace(ref, "-preview.", "-PREVIEW.", 1)
			} else if field == "workflow-prefix" {
				proofs[ref]["workflow"] += ".untrusted.yml"
			} else if field == "workflow-metachar" {
				proofs[ref]["workflow"] = strings.Replace(proofs[ref]["workflow"], "release.yml", "releaseXyml.yml", 1)
			} else {
				proofs[ref][field] = "wrong"
			}
			writeJSON(t, directory, "attestations.json", proofs)
			output, err := runGitHubGate(t, directory)
			if _, markerErr := os.Stat(marker); !os.IsNotExist(markerErr) {
				t.Errorf("unauthenticated preview executed: %v", markerErr)
			}
			if err == nil || !strings.Contains(string(output), "preview provenance") {
				t.Fatalf("error=%v output=%s; want named provenance refusal", err, output)
			}
		})
	}
}

// TestStableGateCommandRunsVerifiedPreviewWithoutCredentials proves authenticated bytes execute only after verification with no inherited credentials through the real CLI and marker preview process.
func TestStableGateCommandRunsVerifiedPreviewWithoutCredentials(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("preview execution requires the Linux release runner")
	}
	directory := githubFixture(t)
	marker := filepath.Join(directory, "executed-preview.json")
	buildFixture(t, filepath.Join(directory, "preview"), "testdata/preview.go", "-ldflags", "-X "+strconv.Quote("main.markerPath="+marker))
	binary, err := os.ReadFile(filepath.Join(directory, "preview"))
	if err != nil {
		t.Fatal(err)
	}
	var proofs map[string]map[string]string
	readJSON(t, directory, "attestations.json", &proofs)
	proofs["refs/tags/v1.0.0-preview.20261012.1"]["sha256"] = fmt.Sprintf("%x", sha256.Sum256(binary))
	writeJSON(t, directory, "attestations.json", proofs)
	command := exec.Command("go", "run", ".", "--tag", "v1.0.0", "--repository", "owner/repo", "--commit", "0123456789abcdef0123456789abcdef01234567", "--asset-hash", strings.Repeat("a", 64))
	command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "GATE_FIXTURE="+directory, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GH_TOKEN=sentinel", "GITHUB_TOKEN=sentinel", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=sentinel", "ACTIONS_ID_TOKEN_REQUEST_URL=https://example.invalid/oidc", "NUGET_OIDC_KEY=sentinel", "NUGET_API_KEY=sentinel", "NPM_TOKEN=sentinel", "NODE_AUTH_TOKEN=sentinel")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "gates passed") {
		t.Fatalf("verified preview: %v %s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var environment []string
	if err := json.Unmarshal(data, &environment); err != nil {
		t.Fatal(err)
	}
	if len(environment) != 0 {
		t.Fatalf("preview inherited %d environment variables", len(environment))
	}
	verified, err := os.ReadFile(filepath.Join(directory, "verified-preview"))
	if err != nil || string(verified) != "refs/tags/v1.0.0-preview.20261012.1" {
		t.Fatalf("wrong verified source: %q %v", verified, err)
	}
	requests, err := os.ReadFile(filepath.Join(directory, "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	fetch, verify := strings.Index(string(requests), "repos/owner/repo/releases/assets/4"), strings.Index(string(requests), "verify refs/tags/v1.0.0-preview.20261012.1")
	if fetch < 0 || verify <= fetch {
		t.Fatalf("preview verification did not follow exact-byte download: %s", requests)
	}
}

// TestStableGateCommandFallsBackAfterUnverifiedPreview proves a candidate's provenance refusal does not hide a later verified preview through the real gate command and fake verifier.
func TestStableGateCommandFallsBackAfterUnverifiedPreview(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("preview execution requires the Linux release runner")
	}
	directory := githubFixture(t)
	var previews []map[string]any
	readJSON(t, directory, "releases.json", &previews)
	first := map[string]any{"tag_name": "v1.0.0-preview.20261012.2", "prerelease": true, "assets": previews[0]["assets"]}
	writeJSON(t, directory, "releases.json", append([]map[string]any{first}, previews...))
	var responses map[string]string
	readJSON(t, directory, "responses.json", &responses)
	responses["repos/owner/repo/commits/v1.0.0-preview.20261012.2"] = "commit.json"
	writeJSON(t, directory, "responses.json", responses)
	output, err := runGitHubGate(t, directory)
	if err != nil || !strings.Contains(string(output), "gates passed") {
		t.Fatalf("verified fallback: %v %s", err, output)
	}
	requests, err := os.ReadFile(filepath.Join(directory, "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	firstTry, secondTry := strings.Index(string(requests), "verify refs/tags/v1.0.0-preview.20261012.2"), strings.Index(string(requests), "verify refs/tags/v1.0.0-preview.20261012.1")
	if firstTry < 0 || secondTry <= firstTry {
		t.Fatalf("candidate provenance ordering lost: %s", requests)
	}
}

// writeJSON marshals value into a named fixture file under directory.
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

// runGitHubGate runs the real gate command against fixture GitHub responses with credentials cleared.
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
	if !strings.Contains(text, "\npermissions:\n  contents: read\n  actions: read\n  attestations: read\n\njobs:\n") {
		t.Error("gate workflow root permissions must grant only contents, actions and attestations reads")
	}
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
	for _, required := range []string{"tags: ['v*']", `github.event_name == 'push' || contains(toJSON(inputs), '"tag"')`, "contents: read", "actions: read", "go run ./internal/release/cmd/stable-gate", `STABLE_TAG: ${{ !contains(toJSON(inputs), '"tag"') && github.ref_name || inputs.tag }}`, "--tag \"$STABLE_TAG\"", "go run ./internal/release/cmd/build-release \"${STABLE_TAG#v}\"", "--commit \"$GITHUB_SHA\"", "--asset-hash \"$asset_hash\""} {
		if !strings.Contains(text, required) {
			t.Fatalf("stable workflow lacks %q", required)
		}
	}
	for _, publish := range []string{"contents: write", "packages: write", "attestations: write", "id-token: write", "gh release create", "npm publish", "nuget push"} {
		if strings.Contains(text, publish) {
			t.Fatalf("gate workflow exposes publication %q", publish)
		}
	}
}

// TestStableGateWorkflowInstallsVerifiedGitHubCLI proves the read-only gate installs the pinned verifier before fetching preview provenance through its maintained workflow boundary.
func TestStableGateWorkflowInstallsVerifiedGitHubCLI(t *testing.T) {
	text := stableGateWorkflow(t)
	setup, gate := strings.Index(text, "go run ./internal/release/cmd/setup-gh"), strings.Index(text, "go run ./internal/release/cmd/stable-gate")
	if setup < 0 || gate <= setup {
		t.Fatal("read-only gate must install verified gh before preview provenance verification")
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

// readJSON decodes a named fixture JSON file into target.
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

// TestStableTagWorkflowVerifiesTagCommit proves the tag verification command runs after checkout and the Go setup it needs, and before builds or gate execution, through .github/workflows/stable-release-gate.yml's step ordering.
func TestStableTagWorkflowVerifiesTagCommit(t *testing.T) {
	text := stableGateWorkflow(t)
	checkout := strings.Index(text, "uses: actions/checkout@v7")
	goSetup := strings.Index(text, "uses: actions/setup-go@v7")
	verification := strings.Index(text, "go run ./internal/release/cmd/verify-stable-tag")
	if verification < 0 {
		t.Fatal("stable-release-gate.yml does not call the actual stable-tag verification command")
	}
	build := strings.Index(text, "go run ./internal/release/cmd/build-release")
	gate := strings.Index(text, "go run ./internal/release/cmd/stable-gate")
	if checkout < 0 || goSetup < 0 || build < 0 || gate < 0 || !(checkout < goSetup && goSetup < verification && verification < build && build < gate) {
		t.Fatal("stable-release-gate.yml must order checkout, Go setup, verification command, build, then gate")
	}
}

// stableGateWorkflow reads the stable release gate workflow with normalized line endings.
func stableGateWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../../.github/workflows/stable-release-gate.yml")
	if err != nil {
		t.Fatalf("read stable-release-gate.yml: %v", err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// TestStableGateCommandContinuesAfterTagLookupFailure proves a failed preview lookup yields to valid candidates and follows an invalid candidate's primary refusal through the real command with fake GitHub evidence.
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
			if scenario == "later invalid candidate" {
				candidate := strings.Index(string(output), "live-suite-linux.json: validation file missing from prerelease")
				lookup := strings.Index(string(output), "prerelease tag v1.0.0-preview.20261012.2")
				if candidate < 0 || lookup <= candidate {
					t.Fatalf("output=%s; want candidate validation refusal first, followed by named lookup failure", output)
				}
			}
		})
	}
}

// TestStableTagWorkflowValidatesCalls proves reusable calls are admitted independently of their tag input and only direct previews skip the gate steps through .github/workflows/stable-release-gate.yml; the verification command's own tests prove its classification.
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
		t.Fatal("verifier setup, build and gate steps must only skip classified direct previews")
	}
}

// TestStableGateCommandValidatesBeforeBinaryFetch proves a failed record is named before a missing binary and prevents binary retrieval through the real command with fake GitHub requests.
func TestStableGateCommandValidatesBeforeBinaryFetch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	for _, binary := range []string{"missing", "present"} {
		t.Run(binary, func(t *testing.T) {
			directory := githubFixture(t)
			var record map[string]any
			readJSON(t, directory, "linux.json", &record)
			record["result"] = "fail"
			writeJSON(t, directory, "linux.json", record)
			if binary == "missing" {
				var previews []map[string]any
				readJSON(t, directory, "releases.json", &previews)
				previews[0]["assets"] = previews[0]["assets"].([]any)[:3]
				writeJSON(t, directory, "releases.json", previews)
			}
			output, err := runGitHubGate(t, directory)
			if err == nil || !strings.Contains(string(output), "live-suite-linux.json: validation gate did not pass") {
				t.Fatalf("error=%v output=%s; want primary record failure before binary", err, output)
			}
			requests, err := os.ReadFile(filepath.Join(directory, "requests.log"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(requests), "repos/owner/repo/releases/assets/4") {
				t.Fatal("failing validation fetched the preview binary")
			}
		})
	}
}

// TestStableGateCommandRetainsAllRefusals proves candidate, version, and tag lookup failures remain named in that priority order through the real command with fake GitHub evidence.
func TestStableGateCommandRetainsAllRefusals(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GitHub adapter uses the Linux preview")
	}
	directory := githubFixture(t)
	var previews []map[string]any
	readJSON(t, directory, "releases.json", &previews)
	previews[0]["assets"] = []any{}
	unresolved := map[string]any{"tag_name": "v1.0.0-preview.20261012.2", "prerelease": true, "assets": []any{}}
	wrong := map[string]any{"tag_name": "v2.0.0-preview.20261012.1", "prerelease": true, "assets": []any{}}
	writeJSON(t, directory, "releases.json", append([]map[string]any{unresolved, wrong}, previews...))
	var responses map[string]string
	readJSON(t, directory, "responses.json", &responses)
	responses["repos/owner/repo/commits/v2.0.0-preview.20261012.1"] = "commit.json"
	writeJSON(t, directory, "responses.json", responses)
	output, err := runGitHubGate(t, directory)
	candidate := strings.Index(string(output), "live-suite-linux.json: validation file missing")
	version := strings.Index(string(output), "prerelease version:")
	lookup := strings.Index(string(output), "prerelease tag v1.0.0-preview.20261012.2:")
	if err == nil || candidate < 0 || version <= candidate || lookup <= version {
		t.Fatalf("error=%v output=%s; want candidate, version, and lookup refusals in order", err, output)
	}
}
