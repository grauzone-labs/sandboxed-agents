package scripts_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestPreviewPublication proves a preview publishes all five attested files and committed notes without registry calls through the real publication command and fake external processes.
func TestPreviewPublication(t *testing.T) {
	f := publicationFixture(t, "1.0.0-preview.20261017.1")
	output, err := f.run(t)
	if err != nil {
		t.Fatalf("preview publication: %v\n%s", err, output)
	}
	state := f.state(t)
	if state["draft"] != false || state["prerelease"] != true {
		t.Fatalf("release state = %#v; want published prerelease", state)
	}
	assets := state["assets"].([]any)
	if len(assets) != 5 || !strings.Contains(state["body"].(string), "Reviewed version notes") {
		t.Fatalf("release = %#v; want five assets and committed notes", state)
	}
	log := f.log(t)
	if strings.Contains(log, `"tool":"npm"`) || strings.Contains(log, `"tool":"dotnet"`) || strings.Contains(log, `"tool":"curl"`) {
		t.Fatalf("preview contacted a registry:\n%s", log)
	}
	if strings.Count(log, `"attestation","verify"`) != 5 {
		t.Fatalf("want provenance verification for all five files:\n%s", log)
	}
}

// TestStablePublication proves the existing gate precedes every publication mutation and stable publication completes both registries before exposing the release through real commands and fake external processes.
func TestStablePublication(t *testing.T) {
	f := publicationFixture(t, "1.0.0")
	output, err := f.run(t)
	if err != nil {
		t.Fatalf("stable publication: %v\n%s", err, output)
	}
	state := f.state(t)
	if state["draft"] != false || state["prerelease"] != false || len(state["assets"].([]any)) != 5 {
		t.Fatalf("release = %#v; want completed stable release", state)
	}
	for _, gate := range []string{"Offline suite", "Live suite (Linux and Windows 11)", "Manual checklist"} {
		if !strings.Contains(state["body"].(string), gate+": `"+f.commit+"`") {
			t.Fatalf("release notes omit %s commit: %s", gate, state["body"])
		}
	}
	log := f.log(t)
	gate := strings.Index(log, "/actions/runs/10/jobs")
	create := strings.Index(log, `"release","create"`)
	npm := strings.Index(log, `"tool":"npm"`)
	nuget := strings.Index(log, `"tool":"dotnet"`)
	finish := strings.Index(log, `"release","edit"`)
	if gate < 0 || create < gate || npm < create || nuget < npm || finish < nuget {
		t.Fatalf("publication order bypasses gate or registries:\n%s", log)
	}
	if !strings.Contains(log, `"--provenance"`) || !strings.Contains(log, `"--api-key","fake-short-lived-key"`) || strings.Contains(log, "--skip-duplicate") {
		t.Fatalf("publication omits OIDC provenance or accepts unchecked duplicates:\n%s", log)
	}
}

// TestPublicationResumesInterruptedRegistries proves a NuGet failure leaves a draft and a retry compares published bytes, skips npm, and completes the release through the real command against fake services.
func TestPublicationResumesInterruptedRegistries(t *testing.T) {
	f := publicationFixture(t, "1.0.0")
	ciWrite(t, f.services, "fail", "dotnet nuget push")
	output, err := f.run(t)
	if err == nil || !strings.Contains(string(output), "NuGet publication") || f.state(t)["draft"] != true {
		t.Fatalf("interrupted publication: error=%v output=%s; want NuGet failure with draft", err, output)
	}
	if err := os.Remove(filepath.Join(f.services, "fail")); err != nil {
		t.Fatal(err)
	}
	output, err = f.run(t)
	if err != nil || f.state(t)["draft"] != false {
		t.Fatalf("recovery: error=%v output=%s; want completed release", err, output)
	}
	log := f.log(t)
	if strings.Count(log, `"tool":"npm"`) != 1 || strings.Count(log, `"release","create"`) != 1 || strings.Count(log, `"release","upload"`) != 1 {
		t.Fatalf("recovery replaced an existing release, asset, or npm version:\n%s", log)
	}
}

// TestPublicationWorkflowWiring proves tag pushes rebuild and gate before publication and privileged external actions use immutable commits through the maintained Actions workflow.
func TestPublicationWorkflowWiring(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".github/workflows/release.yml"))
	if err != nil {
		t.Fatalf("tag publication workflow is unavailable: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"  push:\n    tags: ['v*']", "cancel-in-progress: false", "uses: ./.github/workflows/stable-release-gate.yml", "needs: stable_gate", "needs.stable_gate.result == 'success'", "runs-on: ubuntu-24.04", "ref: ${{ github.sha }}", "fetch-depth: 0", "persist-credentials: false", "go-version-file: go.mod", "node-version: '24'", "npm install --global npm@11.5.1", "gh_2.83.2_linux_amd64.tar.gz", "ca6e7641214fbd0e21429cec4b64a7ba626fd946d8f9d6d191467545b092015e", "id-token: write", "attestations: write", "push-to-registry: false", "create-storage-record: false", "user: ${{ vars.NUGET_USER }}", "NUGET_OIDC_KEY: ${{ steps.nuget.outputs.NUGET_API_KEY }}",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("publication workflow omits %q", required)
		}
	}
	_, publishJob, found := strings.Cut(text, "\n  publish:\n")
	if !found {
		t.Fatal("workflow omits the privileged publication job")
	}
	// Limit the action policy to this job, stopping at any next job or root key.
	if end := regexp.MustCompile(`(?m)^(?:\S|  \S)`).FindStringIndex(publishJob); end != nil {
		publishJob = publishJob[:end[0]]
	}
	uses := regexp.MustCompile(`(?m)^[ \t]+(?:- )?uses:[ \t]+([^ \t\r\n#]+)(?:[ \t]+#[ \t]*([^\r\n]*))?[ \t]*$`).FindAllStringSubmatch(publishJob, -1)
	wantActions := []struct{ repository, version string }{
		{"actions/checkout", "v7"},
		{"actions/setup-go", "v7"},
		{"actions/setup-node", "v7"},
		{"actions/setup-dotnet", "v5"},
		{"actions/attest", "v4"},
		{"NuGet/login", "v1"},
	}
	if len(uses) != len(wantActions) {
		t.Fatalf("publication has %d action references; want the six agreed external steps", len(uses))
	}
	commitPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for i, action := range uses {
		repository, ref, hasRef := strings.Cut(action[1], "@")
		if repository != wantActions[i].repository {
			t.Errorf("publication action %d is %s; want %s in the agreed order", i+1, repository, wantActions[i].repository)
		}
		if !hasRef || !commitPattern.MatchString(ref) {
			t.Errorf("privileged action %s must use an immutable full commit SHA", action[1])
		}
		if !slices.Contains(strings.Fields(action[2]), wantActions[i].version) {
			t.Errorf("publication action %s omits its readable %s version comment", repository, wantActions[i].version)
		}
	}
	last := -1
	for _, step := range []string{"bash scripts/release.sh", "go run ./internal/release/cmd/stable-gate", "uses: actions/attest@", "uses: NuGet/login@", "go run ./internal/release/cmd/publish-release"} {
		next := strings.Index(publishJob, step)
		if next < last || next < 0 {
			t.Errorf("workflow order omits or misorders %q", step)
		}
		last = next
	}
	for _, name := range []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe", "SHA256SUMS", "sandboxed-agents-${{ steps.build.outputs.version }}.tgz", "SandboxedAgents.${{ steps.build.outputs.version }}.nupkg"} {
		if !strings.Contains(text, "${{ runner.temp }}/release-output/"+name) {
			t.Errorf("attestation omits %s", name)
		}
	}
	for _, forbidden := range []string{"secrets.NPM_TOKEN", "secrets.NUGET_API_KEY", "--skip-duplicate", "--clobber", "continue-on-error:"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("workflow permits unsafe publication: %s", forbidden)
		}
	}
}

// TestPublicationRecoversRepositorySignedNuGet proves a verified repository signature may be added without permitting changed package content through real recovery commands and fake registry processes.
func TestPublicationRecoversRepositorySignedNuGet(t *testing.T) {
	f := publicationFixture(t, "1.0.0")
	ciWrite(t, f.services, "fail", "gh release edit")
	if output, err := f.run(t); err == nil {
		t.Fatalf("want interrupted final publication, got %s", output)
	}
	if err := os.Remove(filepath.Join(f.services, "fail")); err != nil {
		t.Fatal(err)
	}
	unsigned, err := os.ReadFile(filepath.Join(f.services, "nuget.nupkg"))
	if err != nil {
		t.Fatal(err)
	}
	signed := repositorySignedFixture(t, unsigned, false)
	if err := os.WriteFile(filepath.Join(f.services, "nuget.nupkg"), signed, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t)
	if err != nil || f.state(t)["draft"] != false {
		t.Fatalf("signed NuGet recovery: error=%v output=%s", err, output)
	}
	if !strings.Contains(f.log(t), `"nuget","verify"`) {
		t.Fatal("signed NuGet download was not verified")
	}
}

// TestPublicationRequiresOIDC proves stable publication refuses a missing npm OIDC identity before mutations through the actual command and fake service boundaries.
func TestPublicationRequiresOIDC(t *testing.T) {
	f := publicationFixture(t, "1.0.0")
	output, err := f.runWithEnvironment(t, []string{"ACTIONS_ID_TOKEN_REQUEST_URL=", "ACTIONS_ID_TOKEN_REQUEST_TOKEN="})
	if err == nil || !strings.Contains(string(output), "npm OIDC") {
		t.Fatalf("missing OIDC: error=%v output=%s", err, output)
	}
	if strings.Contains(f.log(t), `"release","create"`) {
		t.Fatal("created a release without npm OIDC")
	}
}

// TestPublicationIgnoresRegistryTokenFallback proves npm receives no caller token or user configuration through the real publication process boundary.
func TestPublicationIgnoresRegistryTokenFallback(t *testing.T) {
	f := publicationFixture(t, "1.0.0")
	output, err := f.runWithEnvironment(t, []string{"NODE_AUTH_TOKEN=unused-token", "NPM_TOKEN=unused-token", "NPM_CONFIG_USERCONFIG=/unused/npmrc"})
	if err != nil {
		t.Fatalf("OIDC publication: %v %s", err, output)
	}
	log := f.log(t)
	if strings.Contains(log, `"fallback_tokens_present":true`) || !strings.Contains(log, `"--userconfig"`) || !strings.Contains(log, `"--globalconfig"`) {
		t.Fatalf("npm can use fallback credentials:\n%s", log)
	}
}

// TestPublicationFailsClosed proves missing or failed gates, invalid artifacts, absent provenance and registry uncertainty publish nothing through the real command with fake external boundaries.
func TestPublicationFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		change       func(*testing.T, publicationTestFixture)
	}{
		{"missing preview", "prerelease", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.services, "preview-disabled", "yes") }},
		{"failed live record", "live-suite-linux.json", func(t *testing.T, f publicationTestFixture) {
			ciWrite(t, f.services, "record-1.json", `{"result":"fail"}`)
		}},
		{"failed manual record", "manual-checklist.json", func(t *testing.T, f publicationTestFixture) {
			ciWrite(t, f.services, "record-3.json", `{"result":"fail"}`)
		}},
		{"failed CI", "CI", func(t *testing.T, f publicationTestFixture) {
			ciWrite(t, f.services, "runs.json", `{"workflow_runs":[]}`)
		}},
		{"different asset hash", "asset hash", func(t *testing.T, f publicationTestFixture) {
			ciWrite(t, f.services, "preview", "#!/bin/sh\nprintf 'asset-sha256 "+strings.Repeat("f", 64)+"\\n'\n")
		}},
		{"missing attestation", "provenance", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.services, "fail", "gh attestation verify") }},
		{"registry outage", "HTTP 503", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.services, "http-status", "503") }},
		{"registry transport failure", "registry lookup", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.services, "fail", "curl --silent") }},
		{"different npm bytes", "differs from rebuilt bytes", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.services, "npm.tgz", "foreign version") }},
		{"tampered NuGet content", "content differs", func(t *testing.T, f publicationTestFixture) {
			data, err := os.ReadFile(filepath.Join(f.output, "SandboxedAgents.1.0.0.nupkg"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.services, "nuget.nupkg"), repositorySignedFixture(t, data, true), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unverified NuGet signature", "signature verification", func(t *testing.T, f publicationTestFixture) {
			data, err := os.ReadFile(filepath.Join(f.output, "SandboxedAgents.1.0.0.nupkg"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.services, "nuget.nupkg"), repositorySignedFixture(t, data, false), 0600); err != nil {
				t.Fatal(err)
			}
			ciWrite(t, f.services, "fail", "dotnet nuget verify")
		}},
		{"wrong binary checksum", "SHA256SUMS", func(t *testing.T, f publicationTestFixture) { ciWrite(t, f.output, "SHA256SUMS", "incorrect\n") }},
		{"missing release file", "must be a regular file", func(t *testing.T, f publicationTestFixture) {
			if err := os.Remove(filepath.Join(f.output, "sandboxed-agents-1.0.0.tgz")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := publicationFixture(t, "1.0.0")
			test.change(t, f)
			output, err := f.run(t)
			if err == nil || !strings.Contains(string(output), test.reason) {
				t.Fatalf("error=%v output=%s; want %s refusal", err, output, test.reason)
			}
			assertNoPublicationMutation(t, f.log(t))
		})
	}
}

// TestPublicationRecoversPartialGitHubState proves abandoned drafts, missing public assets and packages without a release resume without overwriting bytes through the actual publication command and fake services.
func TestPublicationRecoversPartialGitHubState(t *testing.T) {
	for _, scenario := range []string{"abandoned draft", "public release missing file", "packages without release"} {
		t.Run(scenario, func(t *testing.T) {
			f := publicationFixture(t, "1.0.0")
			if scenario == "abandoned draft" {
				ciWrite(t, f.services, "fail", "gh release upload")
			}
			output, err := f.run(t)
			if scenario == "abandoned draft" {
				if err == nil {
					t.Fatal("wanted upload interruption")
				}
				if err := os.Remove(filepath.Join(f.services, "fail")); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatalf("first publication: %v %s", err, output)
			}
			if scenario == "public release missing file" {
				state := f.state(t)
				state["assets"] = state["assets"].([]any)[:4]
				data, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.services, "release.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "packages without release" {
				if err := os.Remove(filepath.Join(f.services, "release.json")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(f.services, "commands.jsonl")); err != nil {
				t.Fatal(err)
			}
			output, err = f.run(t)
			if err != nil {
				t.Fatalf("recovery: %v %s", err, output)
			}
			state := f.state(t)
			if state["draft"] != false || len(state["assets"].([]any)) != 5 {
				t.Fatalf("incomplete recovered release: %#v", state)
			}
			log := f.log(t)
			if scenario != "abandoned draft" && (strings.Contains(log, `"tool":"npm"`) || strings.Contains(log, `"nuget","push"`)) {
				t.Fatalf("republished immutable registry version:\n%s", log)
			}
			if strings.Contains(log, "--clobber") {
				t.Fatal("overwrote a published file")
			}
		})
	}
}

// TestPublicationRefusesForeignReleaseState proves differing existing files or notes stop recovery before any writes through the real command and fake GitHub state.
func TestPublicationRefusesForeignReleaseState(t *testing.T) {
	for _, scenario := range []string{"asset bytes", "release notes"} {
		t.Run(scenario, func(t *testing.T) {
			f := publicationFixture(t, "1.0.0")
			if output, err := f.run(t); err != nil {
				t.Fatalf("initial publication: %v %s", err, output)
			}
			if scenario == "asset bytes" {
				ciWrite(t, f.services, "asset-100", "foreign bytes")
			} else {
				state := f.state(t)
				state["body"] = "foreign notes"
				data, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.services, "release.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(f.services, "commands.jsonl")); err != nil {
				t.Fatal(err)
			}
			if output, err := f.run(t); err == nil {
				t.Fatalf("foreign state accepted: %s", output)
			}
			assertNoPublicationMutation(t, f.log(t))
		})
	}
}

// TestPublicationRefusesMalformedReleaseList proves empty or null GitHub replies cannot be mistaken for an absent release through the actual preview publication command and fake API boundary.
func TestPublicationRefusesMalformedReleaseList(t *testing.T) {
	for _, response := range []string{"null", ""} {
		t.Run(response, func(t *testing.T) {
			f := publicationFixture(t, "1.0.0-preview.20261017.1")
			ciWrite(t, f.services, "releases-response", response)
			output, err := f.run(t)
			if err == nil || !strings.Contains(string(output), "invalid GitHub release response") {
				t.Fatalf("malformed API: error=%v output=%s", err, output)
			}
			assertNoPublicationMutation(t, f.log(t))
		})
	}
}

// TestPublicationRequiresTaggedNotes proves the exact clean tagged commit and its committed version notes are required before contacting publication services through the real Git and publication commands.
func TestPublicationRequiresTaggedNotes(t *testing.T) {
	for _, scenario := range []string{"dirty checkout", "different commit", "absent notes"} {
		t.Run(scenario, func(t *testing.T) {
			f := publicationFixture(t, "1.0.0-preview.20261017.1")
			reason := "clean Git worktree"
			switch scenario {
			case "dirty checkout":
				ciWrite(t, f.checkout, "untracked.txt", "unvalidated")
			case "different commit":
				ciWrite(t, f.checkout, "new.txt", "new commit")
				f.commit = ciCommit(t, f.checkout)
				reason = "does not name the release commit"
			case "absent notes":
				runGit(t, f.checkout, "rm", "docs/release-notes/1.0.0.md")
				f.commit = ciCommit(t, f.checkout)
				runGit(t, f.checkout, "tag", "--force", "v"+f.version)
				reason = "release notes"
			}
			output, err := f.run(t)
			if err == nil || !strings.Contains(string(output), reason) {
				t.Fatalf("invalid input: error=%v output=%s; want %s", err, output, reason)
			}
			if f.log(t) != "" {
				t.Fatal("contacted external service before validating local inputs")
			}
		})
	}
}

// assertNoPublicationMutation checks that a service transcript contains no release changes or registry pushes.
func assertNoPublicationMutation(t *testing.T, log string) {
	t.Helper()
	for _, mutation := range []string{`"release","create"`, `"release","upload"`, `"release","edit"`, `"tool":"npm"`, `"nuget","push"`} {
		if strings.Contains(log, mutation) {
			t.Fatalf("refusal changed publication state (%s):\n%s", mutation, log)
		}
	}
}

// repositorySignedFixture adds a fake signature at the external registry boundary, optionally tampering with one package entry.
func repositorySignedFixture(t *testing.T, unsigned []byte, tamper bool) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(unsigned), int64(len(unsigned)))
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for i, file := range reader.File {
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(input)
		input.Close()
		if err != nil {
			t.Fatal(err)
		}
		if tamper && i == 0 {
			data = append(data, []byte("tampered")...)
		}
		entry, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := writer.Create(".signature.p7s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("fake registry signature")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// publicationTestFixture owns an isolated tagged checkout, real build output, and fake external service state.
type publicationTestFixture struct{ checkout, output, services, version, commit string }

// publicationFixture builds the real release script in a clean tagged checkout and installs fake GitHub and registry processes on PATH.
func publicationFixture(t *testing.T, version string) publicationTestFixture {
	t.Helper()
	root := releaseRepositoryOrSkip(t)
	checkout := createReleaseRepository(t, root)
	ciWrite(t, checkout, "docs/release-notes/1.0.0.md", "Reviewed version notes\n")
	commit := ciCommit(t, checkout)
	runGit(t, checkout, "tag", "v"+version)
	f := publicationTestFixture{checkout: checkout, output: t.TempDir(), services: t.TempDir(), version: version, commit: commit}
	command := exec.Command("bash", filepath.Join(checkout, "scripts/release.sh"), version, f.output)
	setReleaseTestDirectoryAndEnvironment(command, checkout)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("publication build: %v\n%s", err, output)
	}
	command = exec.Command("go", "build", "-o", filepath.Join(f.services, "tools"), "testdata/publication-tools.go")
	command.Env = releaseTestEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("publication external fixture: %v\n%s", err, output)
	}
	for _, tool := range []string{"gh", "npm", "dotnet", "curl"} {
		if err := os.Link(filepath.Join(f.services, "tools"), filepath.Join(f.services, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// run invokes the maintained publication command with the real compiler and external credentials cleared.
func (f publicationTestFixture) run(t *testing.T, extra ...string) ([]byte, error) {
	t.Helper()
	return f.runWithEnvironment(t, nil, extra...)
}

// runWithEnvironment invokes publication with explicit external service environment overrides for failure cases.
func (f publicationTestFixture) runWithEnvironment(t *testing.T, environment []string, extra ...string) ([]byte, error) {
	t.Helper()
	args := []string{"run", "./internal/release/cmd/publish-release", "--tag", "v" + f.version, "--repository", "grauzone-labs/sandboxed-agents", "--commit", f.commit, "--directory", f.output}
	command := exec.Command("go", append(args, extra...)...)
	command.Dir = f.checkout
	command.Env = append(releaseTestEnvironment(), "PATH="+f.services+string(os.PathListSeparator)+os.Getenv("PATH"), "PUBLICATION_FIXTURE="+f.services, "FIXTURE_OUTPUT="+f.output, "FIXTURE_COMMIT="+f.commit, "FIXTURE_VERSION="+f.version, "GH_TOKEN=", "GITHUB_TOKEN=", "NODE_AUTH_TOKEN=", "NPM_TOKEN=", "NUGET_API_KEY=", "NUGET_OIDC_KEY=fake-short-lived-key")
	command.Env = append(command.Env, "ACTIONS_ID_TOKEN_REQUEST_URL=https://example.invalid/oidc", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=fake-oidc-request")
	command.Env = append(command.Env, environment...)
	return command.CombinedOutput()
}

// state reads the fake GitHub release left by the real publication command.
func (f publicationTestFixture) state(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.services, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// log reads the fake processes' command transcript at the external publication boundary.
func (f publicationTestFixture) log(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.services, "commands.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}
