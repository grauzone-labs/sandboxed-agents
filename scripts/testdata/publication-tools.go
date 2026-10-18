// Command publication-tools supplies fake GitHub and registry process boundaries for release tests.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// main records each external command and handles only the service operations the publication contract permits.
func main() {
	tool, args := filepath.Base(os.Args[0]), os.Args[1:]
	log, err := os.OpenFile(path("commands.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	check(err)
	entry := map[string]any{"tool": tool, "args": args}
	if tool == "npm" {
		entry["fallback_tokens_present"] = os.Getenv("NODE_AUTH_TOKEN") != "" || os.Getenv("NPM_TOKEN") != "" || os.Getenv("NPM_CONFIG_USERCONFIG") != ""
	}
	check(json.NewEncoder(log).Encode(entry))
	check(log.Close())
	if failure, _ := os.ReadFile(path("fail")); len(failure) > 0 && strings.Contains(tool+" "+strings.Join(args, " "), strings.TrimSpace(string(failure))) {
		fmt.Fprintln(os.Stderr, "simulated external failure")
		os.Exit(1)
	}
	switch tool {
	case "gh":
		github(args)
	case "curl":
		if status, err := os.ReadFile(path("http-status")); err == nil {
			fmt.Print(strings.TrimSpace(string(status)))
			return
		}
		name := "npm.tgz"
		if strings.Contains(strings.Join(args, " "), "api.nuget.org") {
			name = "nuget.nupkg"
		}
		data, err := os.ReadFile(path(name))
		if os.IsNotExist(err) {
			fmt.Print("404")
			return
		}
		check(err)
		check(os.WriteFile(option(args, "--output"), data, 0600))
		fmt.Print("200")
	case "npm":
		if len(args) < 2 || args[0] != "publish" {
			panic("unexpected npm")
		}
		copyFile(args[1], path("npm.tgz"))
	case "dotnet":
		if len(args) > 1 && args[0] == "nuget" && args[1] == "verify" {
			return
		}
		if len(args) < 3 || args[0] != "nuget" || args[1] != "push" {
			panic("unexpected dotnet")
		}
		copyFile(args[2], path("nuget.nupkg"))
	default:
		panic("unexpected tool")
	}
}

// github implements releases and attestation reads and writes using fixture files.
func github(args []string) {
	if args[0] == "--version" {
		fmt.Println("gh version 2.102.0")
		return
	}
	if args[0] == "attestation" && args[1] == "verify" {
		verifyArtifact(args[2:])
		return
	}
	if args[0] == "api" {
		endpoint := args[1]
		if strings.Contains(endpoint, "/releases?") {
			if data, err := os.ReadFile(path("releases-response")); err == nil {
				_, err = os.Stdout.Write(data)
				check(err)
				return
			}
			var releases []any
			_, disabled := os.Stat(path("preview-disabled"))
			if os.Getenv("FIXTURE_VERSION") == "1.0.0" && os.IsNotExist(disabled) {
				releases = append(releases, map[string]any{"tag_name": "v1.0.0-preview.20261017.1", "target_commitish": os.Getenv("FIXTURE_COMMIT"), "draft": false, "prerelease": true, "assets": []any{map[string]any{"id": 1, "name": "live-suite-linux.json"}, map[string]any{"id": 2, "name": "live-suite-windows-11.json"}, map[string]any{"id": 3, "name": "manual-checklist.json"}, map[string]any{"id": 4, "name": "sandboxed-agents-linux-amd64"}}})
			}
			if data, err := os.ReadFile(path("release.json")); err == nil {
				var release any
				check(json.Unmarshal(data, &release))
				releases = append(releases, release)
			}
			if releases == nil {
				releases = []any{}
			}
			check(json.NewEncoder(os.Stdout).Encode(releases))
			return
		}
		if strings.Contains(endpoint, "/releases/assets/") {
			id := filepath.Base(endpoint)
			if id == "4" {
				data, err := os.ReadFile(path("preview"))
				if os.IsNotExist(err) {
					data, err = os.ReadFile(filepath.Join(os.Getenv("FIXTURE_OUTPUT"), "sandboxed-agents-linux-amd64"))
				}
				check(err)
				_, err = os.Stdout.Write(data)
				check(err)
				return
			}
			if id == "1" || id == "2" || id == "3" {
				if data, err := os.ReadFile(path("record-" + id + ".json")); err == nil {
					_, err = os.Stdout.Write(data)
					check(err)
					return
				}
				record := map[string]any{"schema_version": 2, "kind": "live-suite", "commit": os.Getenv("FIXTURE_COMMIT"), "platform": "linux", "result": "pass", "image_part_selected": true, "image_part_ran": true, "image_coverage_complete": true, "checks": []any{}}
				if id == "2" {
					record["platform"] = "windows-11"
				}
				if id == "3" {
					record = map[string]any{"schema_version": 1, "kind": "manual-checklist", "commit": os.Getenv("FIXTURE_COMMIT"), "result": "pass", "checks": []any{map[string]any{"name": "agent-login", "result": "pass", "date": "2026-10-17", "platform": "linux"}}}
				}
				check(json.NewEncoder(os.Stdout).Encode(record))
				return
			}
			data, err := os.ReadFile(path("asset-" + filepath.Base(endpoint)))
			check(err)
			_, err = os.Stdout.Write(data)
			check(err)
			return
		}
		if strings.Contains(endpoint, "/commits/") {
			check(json.NewEncoder(os.Stdout).Encode(map[string]any{"sha": os.Getenv("FIXTURE_COMMIT")}))
			return
		}
		if strings.Contains(endpoint, "/actions/workflows/") {
			if data, err := os.ReadFile(path("runs.json")); err == nil {
				_, err = os.Stdout.Write(data)
				check(err)
				return
			}
			check(json.NewEncoder(os.Stdout).Encode(map[string]any{"workflow_runs": []any{map[string]any{"id": 10, "head_sha": os.Getenv("FIXTURE_COMMIT"), "status": "completed", "conclusion": "success"}}}))
			return
		}
		if strings.Contains(endpoint, "/actions/runs/10/jobs") {
			check(json.NewEncoder(os.Stdout).Encode(map[string]any{"jobs": []any{map[string]any{"name": "linux", "status": "completed", "conclusion": "success"}, map[string]any{"name": "windows", "status": "completed", "conclusion": "success"}}}))
			return
		}
		panic("unexpected GitHub endpoint: " + endpoint)
	}
	if args[0] != "release" {
		panic("unexpected gh operation")
	}
	switch args[1] {
	case "create":
		data, err := os.ReadFile(option(args, "--notes-file"))
		check(err)
		writeState(map[string]any{"id": 50, "tag_name": args[2], "target_commitish": option(args, "--target"), "draft": true, "prerelease": has(args, "--prerelease"), "body": string(data), "assets": []any{}})
	case "upload":
		state := readState()
		assets := state["assets"].([]any)
		for _, name := range args[3:] {
			if strings.HasPrefix(name, "--") {
				break
			}
			id := len(assets) + 100
			copyFile(name, path(fmt.Sprint("asset-", id)))
			assets = append(assets, map[string]any{"id": id, "name": filepath.Base(name)})
		}
		state["assets"] = assets
		writeState(state)
	case "edit":
		state := readState()
		state["draft"] = false
		writeState(state)
	default:
		panic("unexpected gh release operation")
	}
}

// verifyArtifact requires expected source bindings and exact fixture bytes for every attestation, and nonexecutable bytes for the preview downloaded by the gate.
func verifyArtifact(args []string) {
	if len(args) == 0 {
		panic("missing attestation subject")
	}
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	repo := flags.String("repo", "", "")
	workflow := flags.String("signer-workflow", "", "")
	commit := flags.String("source-digest", "", "")
	ref := flags.String("source-ref", "", "")
	deny := flags.Bool("deny-self-hosted-runners", false, "")
	check(flags.Parse(args[1:]))
	if flags.NArg() != 0 || *repo != "grauzone-labs/sandboxed-agents" || *workflow != "grauzone-labs/sandboxed-agents/.github/workflows/release.yml" || *commit != os.Getenv("FIXTURE_COMMIT") || !*deny {
		panic("incorrect attestation policy")
	}
	var expected []byte
	var err error
	if *ref == "refs/tags/v"+os.Getenv("FIXTURE_VERSION") {
		expected, err = os.ReadFile(filepath.Join(os.Getenv("FIXTURE_OUTPUT"), filepath.Base(args[0])))
	} else if *ref == "refs/tags/v1.0.0-preview.20261017.1" {
		if _, err := os.Stat(path("preview-provenance-fail")); err == nil {
			panic("unverified preview")
		}
		info, err := os.Stat(args[0])
		check(err)
		if info.Mode().Perm()&0111 != 0 {
			panic("preview executable before verification")
		}
		expected, err = os.ReadFile(path("preview"))
		if os.IsNotExist(err) {
			expected, err = os.ReadFile(filepath.Join(os.Getenv("FIXTURE_OUTPUT"), "sandboxed-agents-linux-amd64"))
		}
	} else {
		panic("incorrect attestation source ref")
	}
	check(err)
	actual, err := os.ReadFile(args[0])
	check(err)
	if !bytes.Equal(actual, expected) {
		panic("attestation subject differs from exact downloaded bytes")
	}
}

// path returns a path inside the fake service state directory.
func path(name string) string { return filepath.Join(os.Getenv("PUBLICATION_FIXTURE"), name) }

// option extracts an explicitly supplied CLI option value.
func option(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	panic("missing option: " + name)
}

// has reports whether a command contains an exact flag.
func has(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

// copyFile copies service bytes without interpreting the package or executable.
func copyFile(source, target string) {
	data, err := os.ReadFile(source)
	check(err)
	check(os.WriteFile(target, data, 0600))
}

// readState decodes the fake GitHub release.
func readState() map[string]any {
	data, err := os.ReadFile(path("release.json"))
	check(err)
	var state map[string]any
	check(json.Unmarshal(data, &state))
	return state
}

// writeState saves the fake GitHub release.
func writeState(state map[string]any) {
	data, err := json.Marshal(state)
	check(err)
	check(os.WriteFile(path("release.json"), data, 0600))
}

// check terminates a fake command when its service operation cannot complete.
func check(err error) {
	if err != nil {
		panic(err)
	}
}
