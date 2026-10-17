// Command publish-release publishes already-built release files at external process boundaries.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

// main reports publication failures without publishing a partial success.
func main() {
	if err := publish(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "release publication: %v\n", err)
		os.Exit(1)
	}
}

// publication holds the immutable tag, commit, notes and five build outputs being published.
type publication struct {
	tag, repository, commit, directory, version, notes, hash string
	preview                                                  bool
	files                                                    []string
}

// publishedRelease describes the metadata and assets needed to resume an interrupted publication.
type publishedRelease struct {
	Tag     string `json:"tag_name"`
	Commit  string `json:"target_commitish"`
	Body    string `json:"body"`
	Draft   bool   `json:"draft"`
	Preview bool   `json:"prerelease"`
	Assets  []struct {
		ID   int64
		Name string
	} `json:"assets"`
}

// publish validates local inputs and provenance before changing the GitHub release.
func publish(args []string) error {
	flags := flag.NewFlagSet("publish-release", flag.ContinueOnError)
	p := publication{}
	flags.StringVar(&p.tag, "tag", "", "immutable vX.Y.Z[-preview.YYYYMMDD.N] tag")
	flags.StringVar(&p.repository, "repository", "", "GitHub owner/repository")
	flags.StringVar(&p.commit, "commit", "", "full tagged commit SHA")
	flags.StringVar(&p.directory, "directory", "", "release.sh output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if err := p.validate(); err != nil {
		return err
	}
	if !p.preview {
		if os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") == "" {
			return fmt.Errorf("npm OIDC requires the GitHub Actions id-token: write identity")
		}
		if os.Getenv("NUGET_OIDC_KEY") == "" {
			return fmt.Errorf("NuGet OIDC login must supply its short-lived key")
		}
		gate := exec.Command("go", "run", "./internal/release/cmd/stable-gate", "--tag", p.tag, "--repository", p.repository, "--commit", p.commit, "--asset-hash", p.hash)
		output, err := gate.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s", bytes.TrimSpace(output))
		}
		p.notes += "\n\nValidation gates (passed on this release commit):\n\n"
		for _, gate := range []string{"Offline suite", "Live suite (Linux and Windows 11)", "Manual checklist"} {
			p.notes += fmt.Sprintf("- %s: `%s`\n", gate, p.commit)
		}
	}
	for _, name := range p.files {
		if _, err := command("gh", "attestation", "verify", filepath.Join(p.directory, name), "--repo", p.repository,
			"--signer-workflow", p.repository+"/.github/workflows/release.yml", "--source-digest", p.commit,
			"--source-ref", "refs/tags/"+p.tag, "--deny-self-hosted-runners"); err != nil {
			return fmt.Errorf("%s provenance: %w", name, err)
		}
	}
	state, err := p.existingRelease()
	if err != nil {
		return err
	}
	missing, err := p.compareAssets(state)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	if !p.preview {
		existing, err = p.compareRegistries()
		if err != nil {
			return err
		}
	}
	return p.createAndPublish(state, missing, existing)
}

// validate refuses untagged or dirty checkouts, uncommitted notes, missing files and mismatched executable checksums.
func (p *publication) validate() error {
	p.version = release.PreviewTagVersion(p.tag)
	p.preview = p.version != ""
	if !p.preview {
		var err error
		p.version, err = release.StableTagVersion(p.tag)
		if err != nil {
			return err
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(p.repository) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.commit) {
		return fmt.Errorf("repository and full commit SHA are required")
	}
	for _, ref := range []string{"HEAD", "refs/tags/" + p.tag + "^{commit}"} {
		data, err := command("git", "rev-parse", "--verify", ref)
		if err != nil || strings.TrimSpace(string(data)) != p.commit {
			return fmt.Errorf("%s does not name the release commit", ref)
		}
	}
	status, err := command("git", "status", "--porcelain", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return fmt.Errorf("publication requires a clean Git worktree")
	}
	data, err := command("git", "show", p.commit+":docs/release-notes/"+p.version+".md")
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("release notes docs/release-notes/%s.md must be committed before the preview", p.version)
	}
	p.notes = string(data)
	p.directory, err = filepath.Abs(p.directory)
	if err != nil {
		return err
	}
	p.files = []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe", "SHA256SUMS", "sandboxed-agents-" + strings.TrimPrefix(p.tag, "v") + ".tgz", "SandboxedAgents." + strings.TrimPrefix(p.tag, "v") + ".nupkg"}
	for _, name := range p.files {
		info, err := os.Lstat(filepath.Join(p.directory, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release file %s must be a regular file", name)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(p.directory, "SHA256SUMS"))
	if err != nil {
		return err
	}
	var want strings.Builder
	for _, name := range p.files[:2] {
		data, err := os.ReadFile(filepath.Join(p.directory, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&want, "%x  %s\n", sha256.Sum256(data), name)
	}
	if string(manifest) != want.String() {
		return fmt.Errorf("SHA256SUMS does not match both rebuilt executables")
	}
	version, err := command(filepath.Join(p.directory, p.files[0]), "version")
	pattern := regexp.MustCompile(`^sandboxed-agents ` + regexp.QuoteMeta(strings.TrimPrefix(p.tag, "v")) + `\nasset-sha256 ([0-9a-f]{64})\n$`)
	match := pattern.FindSubmatch(version)
	if err != nil || match == nil {
		return fmt.Errorf("rebuilt executable version or asset hash differs from the tag")
	}
	p.hash = string(match[1])
	return nil
}

// existingRelease reads all release pages so drafts are visible and an API failure is never treated as absence.
func (p publication) existingRelease() (*publishedRelease, error) {
	data, err := command("gh", "api", "repos/"+p.repository+"/releases?per_page=100", "--paginate")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	pages := 0
	for {
		var page []publishedRelease
		if err := decoder.Decode(&page); err == io.EOF {
			if pages == 0 {
				return nil, fmt.Errorf("invalid GitHub release response")
			}
			return nil, nil
		} else if err != nil {
			return nil, fmt.Errorf("invalid GitHub release response")
		}
		if page == nil {
			return nil, fmt.Errorf("invalid GitHub release response")
		}
		pages++
		for _, state := range page {
			if state.Tag == p.tag {
				return &state, nil
			}
		}
	}
}

// compareAssets refuses foreign release metadata or different bytes and returns only the missing build outputs.
func (p publication) compareAssets(state *publishedRelease) ([]string, error) {
	if state == nil {
		return append([]string(nil), p.files...), nil
	}
	if state.Commit != p.commit || state.Body != p.notes || state.Preview != p.preview {
		return nil, fmt.Errorf("existing release metadata differs from the validated tag, notes or release kind; recovery requires maintainer inspection")
	}
	found := map[string]bool{}
	for _, asset := range state.Assets {
		expected := false
		for _, name := range p.files {
			if asset.Name == name {
				expected = true
			}
		}
		if !expected {
			if p.preview && (asset.Name == "live-suite-linux.json" || asset.Name == "live-suite-windows-11.json" || asset.Name == "manual-checklist.json") {
				continue
			}
			return nil, fmt.Errorf("unexpected release asset %s", asset.Name)
		}
		if found[asset.Name] || asset.ID <= 0 {
			return nil, fmt.Errorf("ambiguous release asset %s", asset.Name)
		}
		found[asset.Name] = true
		data, err := command("gh", "api", fmt.Sprintf("repos/%s/releases/assets/%d", p.repository, asset.ID), "-H", "Accept: application/octet-stream")
		if err != nil {
			return nil, err
		}
		if err := p.compareBytes(asset.Name, data); err != nil {
			return nil, err
		}
	}
	var missing []string
	for _, name := range p.files {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// compareRegistries reads exact immutable versions, comparing npm bytes and verifying NuGet's signed package contents.
func (p publication) compareRegistries() (map[string]bool, error) {
	existing := map[string]bool{}
	urls := []string{"https://registry.npmjs.org/sandboxed-agents/-/sandboxed-agents-" + p.version + ".tgz", "https://api.nuget.org/v3-flatcontainer/sandboxedagents/" + p.version + "/sandboxedagents." + p.version + ".nupkg"}
	for i, url := range urls {
		name := p.files[i+3]
		file, err := os.CreateTemp("", "sandboxed-agents-registry-*."+filepath.Ext(name)[1:])
		if err != nil {
			return nil, err
		}
		path := file.Name()
		if err := file.Close(); err != nil {
			os.Remove(path)
			return nil, err
		}
		status, err := command("curl", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "30", "--max-time", "300", "--output", path, "--write-out", "%{http_code}", url)
		data, readErr := os.ReadFile(path)
		if err != nil {
			os.Remove(path)
			return nil, fmt.Errorf("%s registry lookup: %w", name, err)
		}
		switch string(status) {
		case "404":
		case "200":
			if readErr != nil {
				os.Remove(path)
				return nil, readErr
			}
			var compareErr error
			if i == 1 {
				compareErr = p.compareNuGet(name, path, data)
			} else {
				compareErr = p.compareBytes(name, data)
			}
			if compareErr != nil {
				os.Remove(path)
				return nil, compareErr
			}
			existing[name] = true
		default:
			os.Remove(path)
			return nil, fmt.Errorf("%s registry lookup returned HTTP %s; absence is not proven", name, status)
		}
		os.Remove(path)
	}
	return existing, nil
}

// compareBytes refuses to replace a previously published artifact with different rebuilt bytes.
func (p publication) compareBytes(name string, data []byte) error {
	want, err := os.ReadFile(filepath.Join(p.directory, name))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, want) {
		return fmt.Errorf("published %s differs from rebuilt bytes; do not overwrite immutable versions or tags", name)
	}
	return nil
}

// createAndPublish resumes a matching draft, uploads missing files, completes missing registries and exposes the release last.
func (p publication) createAndPublish(state *publishedRelease, missing []string, existing map[string]bool) error {
	notes, err := os.CreateTemp("", "sandboxed-agents-release-notes-")
	if err != nil {
		return err
	}
	defer os.Remove(notes.Name())
	_, err = notes.WriteString(p.notes)
	closeErr := notes.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	args := []string{"release", "create", p.tag, "--repo", p.repository, "--verify-tag", "--target", p.commit, "--title", p.tag, "--notes-file", notes.Name(), "--draft"}
	if p.preview {
		args = append(args, "--prerelease")
	}
	if state == nil {
		if _, err := command("gh", args...); err != nil {
			return err
		}
	}
	upload := []string{"release", "upload", p.tag}
	for _, name := range missing {
		upload = append(upload, filepath.Join(p.directory, name))
	}
	upload = append(upload, "--repo", p.repository)
	if len(missing) > 0 {
		if _, err := command("gh", upload...); err != nil {
			return err
		}
	}
	if !p.preview {
		if !existing[p.files[3]] {
			if err := p.publishNPM(); err != nil {
				return fmt.Errorf("npm publication: %w", err)
			}
		}
		if !existing[p.files[4]] {
			if _, err := command("dotnet", "nuget", "push", filepath.Join(p.directory, p.files[4]), "--api-key", os.Getenv("NUGET_OIDC_KEY"), "--source", "https://api.nuget.org/v3/index.json"); err != nil {
				return fmt.Errorf("NuGet publication: %w", err)
			}
		}
	}
	if state != nil && !state.Draft {
		return nil
	}
	_, err = command("gh", "release", "edit", p.tag, "--repo", p.repository, "--draft=false", fmt.Sprintf("--prerelease=%t", p.preview), fmt.Sprintf("--latest=%t", !p.preview))
	return err
}

// publishNPM supplies an empty npm configuration and removes caller tokens so publication uses the Actions OIDC identity.
func (p publication) publishNPM() error {
	directory, err := os.MkdirTemp("", "sandboxed-agents-npm-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	user, global := filepath.Join(directory, "user.npmrc"), filepath.Join(directory, "global.npmrc")
	for _, name := range []string{user, global} {
		if err := os.WriteFile(name, nil, 0600); err != nil {
			return err
		}
	}
	process := exec.Command("npm", "publish", filepath.Join(p.directory, p.files[3]), "--registry", "https://registry.npmjs.org", "--access", "public", "--provenance", "--ignore-scripts", "--userconfig", user, "--globalconfig", global)
	// A directory with no project .npmrc also excludes configuration in the checkout.
	process.Dir = directory
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key != "NODE_AUTH_TOKEN" && key != "NPM_TOKEN" && !strings.HasPrefix(key, "NPM_CONFIG_") {
			process.Env = append(process.Env, entry)
		}
	}
	if _, err := process.Output(); err != nil {
		return fmt.Errorf("npm failed; inspect registry state before retrying")
	}
	return nil
}

// command executes a real process, keeping arguments and potentially credential-bearing output out of errors.
func command(name string, args ...string) ([]byte, error) {
	output, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed; inspect the service state before retrying", filepath.Base(name))
	}
	return output, nil
}
