package manualcheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var ghVersionLine = regexp.MustCompile(`gh version (\d+)\.(\d+)\.(\d+)`)

// ValidationRecordNames are the record files a validated preview carries beside its release files.
var ValidationRecordNames = []string{"live-suite-linux.json", "live-suite-windows-11.json", "manual-checklist.json"}

// ghVersionAtLeast parses `gh --version` output and reports whether it is at least major.minor.patch.
func ghVersionAtLeast(output string, major, minor, patch int) (string, bool) {
	match := ghVersionLine.FindStringSubmatch(output)
	if match == nil {
		return "", false
	}
	version := match[1] + "." + match[2] + "." + match[3]
	parts := make([]int, 3)
	for index := range parts {
		parts[index], _ = strconv.Atoi(match[index+1])
	}
	want := []int{major, minor, patch}
	for index := range parts {
		if parts[index] != want[index] {
			return version, parts[index] > want[index]
		}
	}
	return version, true
}

// PreviewAttestation runs item `preview-attestation`: the five files of the preview prerelease,
// a verified attestation for each, the executables' checksums, and the tag's commit against the
// run's commit. It needs gh 2.67.0 or newer, the historical floor for `gh attestation verify`.
func (r *Run) PreviewAttestation(tag, repository string) error {
	if repository == "" {
		repository = "grauzone-labs/sandboxed-agents"
	}
	if tag != "v"+r.Version {
		return fmt.Errorf("tag %s is not the preview tag v%s of this run", tag, r.Version)
	}
	dir, err := r.ItemDir("preview-attestation")
	if err != nil {
		return err
	}
	ghOutput, err := exec.Command("gh", "--version").Output()
	if err != nil {
		return fmt.Errorf("gh --version: %w", err)
	}
	ghVersion, recent := ghVersionAtLeast(string(ghOutput), 2, 67, 0)
	if !recent {
		return fmt.Errorf("gh %q is below 2.67.0, the historical floor for gh attestation verify", ghVersion)
	}
	if err := writePrivate(filepath.Join(dir, "gh-version.txt"), "gh "+ghVersion+"\n"); err != nil {
		return err
	}
	failures := &Failures{run: r}

	// 1. The files of the prerelease.
	assets, err := Step(dir, "assets", nil, exec.Command("gh", "release", "view", tag, "--repo", repository, "--json", "assets", "--jq", ".assets[].name"))
	if err != nil {
		return err
	}
	if assets.Exit != 0 {
		return fmt.Errorf("gh release view exited %d; see %s", assets.Exit, assets.Log)
	}
	names := strings.Fields(assets.Output())
	slices.Sort(names)
	expected := []string{"SHA256SUMS", "SandboxedAgents." + r.Version + ".nupkg", "sandboxed-agents-" + r.Version + ".tgz", "sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe"}
	// Validation records are uploaded to the same prerelease ([Releases](releases.md#validation-records))
	// and carry no build attestation; they are listed beside the five release files, never in their place.
	var records []string
	for _, name := range names {
		switch {
		case slices.Contains(expected, name):
		case slices.Contains(ValidationRecordNames, name):
			records = append(records, name)
		default:
			failures.Addf("the prerelease lists an unexpected file %q", name)
		}
	}
	for _, name := range expected {
		if !slices.Contains(names, name) {
			failures.Addf("the prerelease lacks %s", name)
		}
	}
	if len(records) > 0 {
		r.Printf("validation records present beside the release files: %s\n", strings.Join(records, ", "))
	}
	// 2. Download into a fresh directory and verify each attestation.
	download := filepath.Join(dir, "download")
	if err := os.MkdirAll(download, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(download)
	fetch, err := Step(dir, "download", nil, exec.Command("gh", "release", "download", tag, "--repo", repository, "--dir", download))
	if err != nil {
		return err
	}
	if fetch.Exit != 0 {
		failures.Addf("gh release download exited %d", fetch.Exit)
	}
	verified := 0
	for _, name := range expected {
		if !slices.Contains(names, name) {
			continue
		}
		result, err := Step(dir, "verify-"+name, nil, exec.Command("gh", "attestation", "verify", filepath.Join(download, name), "--repo", repository))
		if err != nil {
			return err
		}
		if result.Exit != 0 {
			failures.Addf("gh attestation verify %s exited %d", name, result.Exit)
			continue
		}
		verified++
	}
	r.Printf("verified attestations: %d\n", verified)
	// 3. The checksums next to the executables, computed here so that the item has the same basis
	// on both platforms and assumes no sha256sum program.
	sums, err := checkSums(download, []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe"})
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "sha256sums.txt"), sums); err != nil {
		return err
	}
	for _, name := range []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe"} {
		if !strings.Contains(sums, name+": OK\n") {
			failures.Addf("%s did not check OK against SHA256SUMS", name)
		}
	}
	// 4. The tag's commit is the run's commit.
	tagCommit, err := Step(dir, "tag-commit", nil, exec.Command("git", "ls-remote", "https://github.com/"+repository+".git", "refs/tags/"+tag+"^{}", "refs/tags/"+tag))
	if err != nil {
		return err
	}
	commit := ""
	for _, line := range strings.Split(tagCommit.Output(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (strings.HasSuffix(fields[1], "^{}") || commit == "") {
			commit = fields[0]
		}
	}
	if commit != r.Commit {
		failures.Addf("tag %s points to %q, not the run commit %s", tag, commit, r.Commit)
	}
	if verified != len(expected) {
		failures.Addf("%d of %d attestations verified", verified, len(expected))
	}
	return failures.Finish(dir, "preview-attestation", fmt.Sprintf("five files listed, five attestations verified with gh %s, both executables OK against SHA256SUMS, tag %s at %s", ghVersion, tag, commit))
}

// checkSums compares the named files in dir with their entries in dir's SHA256SUMS and reports
// them in the form `sha256sum --check` prints: `NAME: OK` or `NAME: FAILED` per file, `NAME: MISSING`
// for a file SHA256SUMS does not list. It errs only when SHA256SUMS itself cannot be read.
func checkSums(dir string, names []string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		return "", err
	}
	expected := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && sha256Pattern.MatchString(fields[0]) {
			expected[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
		}
	}
	var report strings.Builder
	for _, name := range names {
		want, listed := expected[name]
		if !listed {
			report.WriteString(name + ": MISSING\n")
			continue
		}
		got, err := FileSHA256(filepath.Join(dir, name))
		if err != nil || !strings.EqualFold(got, want) {
			report.WriteString(name + ": FAILED\n")
			continue
		}
		report.WriteString(name + ": OK\n")
	}
	return report.String(), nil
}
