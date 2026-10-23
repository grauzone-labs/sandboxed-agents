package manualcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Agents are the catalog's delivered agents, which the persistence rounds observe.
var Agents = []string{"copilot", "claude", "codex", "opencode"}

// PersistenceConfig is the configuration of the persistence items of one run: the sandbox, the
// pinned opencode version, the repositories, and the hash of the Azure account noted at login.
type PersistenceConfig struct {
	Sandbox            string `json:"sandbox"`
	OpencodeVersion    string `json:"opencode_version"`
	GitHubRepo         string `json:"github_repo"`
	AzdoOrganization   string `json:"azdo_organization"`
	AzdoRepo           string `json:"azdo_repo"`
	AzureAccountSHA256 string `json:"azure_account_sha256"`
	SetupUTC           string `json:"setup_utc"`
}

// PersistenceSetupOptions are the inputs of the one-time persistence setup.
type PersistenceSetupOptions struct {
	Sandbox, GitHubRepo, AzdoOrganization, AzdoRepo string
}

var statusVersion = regexp.MustCompile(`(?m)^opencode: enabled; version ([^;]+);`)

// accountHashLine finds the account hash that azAccountProbe prints.
var accountHashLine = regexp.MustCompile(`(?m)^az-account-sha256 ([0-9a-f]{64})$`)

// AccountHash returns the SHA-256 of an account name without surrounding whitespace, the form the
// rounds compare: the in-sandbox script hashes the `az account show` output through printf '%s',
// so no trailing newline is included on either side.
func AccountHash(name string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(name)))
	return hex.EncodeToString(sum[:])
}

// inspectConfig returns the configuration the update must keep, in the documented two lines.
func (r *Run) inspectConfig(sandbox string) (string, error) {
	first, err := r.PodmanOutput("inspect", "--format", `{{.Image}} {{.HostConfig.Memory}} {{.HostConfig.CpuQuota}} {{.HostConfig.CpuPeriod}} {{.HostConfig.PidsLimit}} {{.HostConfig.ShmSize}} {{index .Config.Labels "io.sandboxed-agents.toolchains"}} {{index .Config.Labels "io.sandboxed-agents.port"}}`, r.Container(sandbox))
	if err != nil {
		return "", err
	}
	second, err := r.PodmanOutput("inspect", "--format", `{{range .Mounts}}{{.Name}} {{.Destination}};{{end}}`, r.Container(sandbox))
	if err != nil {
		return "", err
	}
	return first + "\n" + second + "\n", nil
}

// loadPersistence reads the configuration that PersistenceSetup wrote and refuses one whose setup
// noted no Azure account hash.
func (r *Run) loadPersistence() (string, *PersistenceConfig, error) {
	dir, err := r.ItemDir("persistence")
	if err != nil {
		return "", nil, err
	}
	var config PersistenceConfig
	if err := readJSON(filepath.Join(dir, "config.json"), &config); err != nil {
		return "", nil, fmt.Errorf("run the persistence setup first: %w", err)
	}
	// An empty hash would match every account hash a round prints, so a setup that noted no Azure
	// account ends the persistence items of this run.
	if !sha256Pattern.MatchString(config.AzureAccountSHA256) {
		return "", nil, errors.New("the persistence setup noted no Azure account; continue the persistence items in a new run")
	}
	return dir, &config, nil
}

// PersistenceSetup runs the one-time setup of the persistence items: it pins opencode at its
// installed version, configures the Git identity and credential helper, stores the fake
// credential, records the configuration the update must keep, and notes the Azure account by
// its hash only.
func (r *Run) PersistenceSetup(options PersistenceSetupOptions) error {
	if options.Sandbox == "" {
		options.Sandbox = "chk-persist"
	}
	if !namePattern.MatchString(options.Sandbox) || options.GitHubRepo == "" || options.AzdoOrganization == "" || options.AzdoRepo == "" {
		return errors.New("sandbox, GitHub repository, Azure DevOps organization, and repository are required")
	}
	dir, err := r.ItemDir("persistence")
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		return errors.New("the persistence setup already ran for this run")
	}
	status, err := Step(dir, "status-opencode", nil, r.SA("agents", "status", options.Sandbox, "opencode"))
	if err != nil {
		return err
	}
	match := statusVersion.FindStringSubmatch(status.Output())
	if status.Exit != 0 || match == nil {
		return fmt.Errorf("opencode is not enabled in %s; see %s", options.Sandbox, status.Log)
	}
	version := match[1]
	failures := &Failures{run: r}
	steps := []struct {
		name    string
		command *exec.Cmd
	}{
		{"pin-opencode", r.SA("agents", "enable", options.Sandbox, "opencode", "--version", version)},
		{"git-identity", r.SA("integrations", "config", options.Sandbox, "git", "identity", "--name", "Manual Check", "--email", "manual-check@example.invalid")},
		{"git-credentials", r.SA("integrations", "config", options.Sandbox, "git", "credentials")},
	}
	for _, step := range steps {
		result, err := Step(dir, step.name, nil, step.command)
		if err != nil {
			return err
		}
		if result.Exit != 0 {
			failures.Addf("%s exited %d", step.name, result.Exit)
		}
	}
	fake, err := r.ShellStep(dir, "fake-credential", options.Sandbox, `printf 'protocol=https\nhost=example.invalid\nusername=chk\npassword=not-a-secret\n' | git credential approve`)
	if err != nil {
		return err
	}
	if fake.Exit != 0 {
		failures.Addf("storing the fake credential exited %d", fake.Exit)
	}
	before, err := r.inspectConfig(options.Sandbox)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "config-before.txt"), before); err != nil {
		return err
	}
	// The Azure account is compared by hash in every round. The probe hashes the name inside the
	// sandbox, the same way the rounds do, so the name never reaches a file of the run.
	account, err := r.ShellStep(dir, "az-account-hash", options.Sandbox, azAccountProbe)
	if err != nil {
		return err
	}
	hash := ""
	if match := accountHashLine.FindStringSubmatch(account.Output()); match != nil && account.HasLine("az exit 0") {
		hash = match[1]
	} else {
		failures.Addf("az account show selected no account; the Azure login must exist before the setup")
	}
	config := PersistenceConfig{Sandbox: options.Sandbox, OpencodeVersion: version, GitHubRepo: options.GitHubRepo, AzdoOrganization: options.AzdoOrganization, AzdoRepo: options.AzdoRepo, AzureAccountSHA256: hash, SetupUTC: utcNow()}
	if err := writePrivateJSON(filepath.Join(dir, "config.json"), config); err != nil {
		return err
	}
	if failures.Count() > 0 {
		return fmt.Errorf("persistence setup: %d step(s) failed; see %s", failures.Count(), dir)
	}
	r.Printf("setup done: opencode pinned at %s, git identity and credentials configured, config-before.txt recorded\n", version)
	return nil
}

// PersistenceObserve runs the mechanical part of the observation set of one round: the agent
// statuses and the pin, the GitHub and Azure DevOps clones, the Azure account, the two fresh
// commits with their authors, and the stored fake credential. The agent sessions are the
// person's step, printed as the checkpoint.
func (r *Run) PersistenceObserve(round int) error {
	if round < 0 {
		return errors.New("round must be 0 or more")
	}
	base, config, err := r.loadPersistence()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, fmt.Sprintf("round-%d", round))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	failures := &Failures{run: r}
	for _, agent := range Agents {
		status, err := Step(dir, "status-"+agent, nil, r.SA("agents", "status", config.Sandbox, agent))
		if err != nil {
			return err
		}
		output := status.Output()
		if status.Exit != 0 || !strings.HasPrefix(output, agent+": enabled;") {
			failures.Addf("%s is not enabled", agent)
		}
		if strings.Contains(output, "session true") {
			failures.Addf("%s has a running session; stop it before this round", agent)
		}
		if agent == "opencode" && !strings.Contains(output, fmt.Sprintf("opencode: enabled; version %s; pin %s;", config.OpencodeVersion, config.OpencodeVersion)) {
			failures.Addf("opencode is not at version %s with that pin", config.OpencodeVersion)
		}
	}
	script := roundScript(*config, round)
	result, err := r.ShellStep(dir, "in-sandbox", config.Sandbox, script)
	if err != nil {
		return err
	}
	expect := func(text, description string) {
		if result.Contains(text) {
			r.Printf("round %d: %s\n", round, description)
			return
		}
		failures.Addf("round %d: not observed: %s", round, description)
	}
	expect("Logged in to github.com", "gh auth status shows github.com logged in")
	expect("gh-clone exit 0", "GitHub clone without a prompt")
	expect("az-account-sha256 "+config.AzureAccountSHA256, "az account show prints the account noted at login")
	expect("azdo-clone exit 0", "Azure DevOps clone without a prompt")
	expect("azdo-projects exit 0", "az devops project list succeeds")
	if !regexp.MustCompile(`(?m)^azdo-projects \S`).MatchString(result.Output()) {
		failures.Addf("round %d: no project rows listed", round)
	}
	author := fmt.Sprintf("Manual Check <manual-check@example.invalid> round-%d", round)
	expect("repo-author "+author, "the new repository's commit has the configured author")
	expect("gh-author "+author, "the GitHub clone's commit has the configured author")
	if result.HasLine("credential-count 1") {
		r.Printf("round %d: the fake credential is stored once\n", round)
	} else {
		failures.Addf("round %d: not observed: the fake credential is stored exactly once", round)
	}
	if err := r.Checkpoint(dir,
		fmt.Sprintf("For each of %s: run `sandboxed-agents agents session %s AGENT`, send a short prompt such as `say hello`, confirm an answer with no sign-in prompt, detach with Ctrl+b then d.", strings.Join(Agents, ", "), config.Sandbox),
		fmt.Sprintf("End each session: `sandboxed-agents agents session %s AGENT --stop`.", config.Sandbox),
	); err != nil {
		return err
	}
	return failures.Finish(dir, fmt.Sprintf("persistence round %d", round), "mechanical observations as documented; the agent sessions are the person's step")
}

// roundScript returns the in-sandbox script of one persistence round: the GitHub login line and
// clone, the Azure account as its hash, the Azure DevOps clone and project list, fresh commits with
// their authors, and the stored credential count, each with its exit status. Round 0 reuses the
// clones the login items made.
func roundScript(config PersistenceConfig, round int) string {
	clone := fmt.Sprintf("GIT_TERMINAL_PROMPT=0 git clone %s /workspace/gh-%d", config.GitHubRepo, round)
	azdoClone := fmt.Sprintf("GIT_TERMINAL_PROMPT=0 git clone %s /workspace/azdo-%d", config.AzdoRepo, round)
	if round == 0 {
		// Round 0 reuses the clones the login items made; both must exist as repositories.
		clone = "test -d /workspace/gh-0/.git"
		azdoClone = "test -d /workspace/azdo-0/.git"
	}
	// ghStatusProbe holds no formatting verb, so it is joined before the format string.
	return fmt.Sprintf(ghStatusProbe+`
%s; echo "gh-clone exit $?"
printf '%%s' "$(az account show --query user.name --output tsv)" | sha256sum | cut -d' ' -f1 | sed 's/^/az-account-sha256 /'
%s; echo "azdo-clone exit $?"
az devops project list --organization %s --output table | sed 's/^/azdo-projects /'; echo "azdo-projects exit ${PIPESTATUS[0]}"
git init /workspace/repo-%d > /dev/null && git -C /workspace/repo-%d commit --allow-empty -m round-%d > /dev/null; echo "repo-commit exit $?"
git -C /workspace/repo-%d log -1 --format='repo-author %%an <%%ae> %%s'
git -C /workspace/gh-%d commit --allow-empty -m round-%d > /dev/null; echo "gh-commit exit $?"
git -C /workspace/gh-%d log -1 --format='gh-author %%an <%%ae> %%s'
grep -c example.invalid /home/agent/.git-credentials | sed 's/^/credential-count /'`,
		clone, azdoClone, config.AzdoOrganization, round, round, round, round, round, round, round)
}

// PersistenceMakeOutdated saves the image inventory for `no-image-removal`, notes the container
// image and the image its tag names, runs the checklist's one `build`, and checks that the
// sandbox is outdated and the tag names a different image.
func (r *Run) PersistenceMakeOutdated() error {
	dir, config, err := r.loadPersistence()
	if err != nil {
		return err
	}
	if _, err := r.ImagesInventory("images-before"); err != nil {
		return err
	}
	failures := &Failures{run: r}
	tag, err := r.PodmanOutput("inspect", "--format", "{{.Config.Image}}", r.Container(config.Sandbox))
	if err != nil {
		return err
	}
	containerImage, err := r.PodmanOutput("inspect", "--format", "{{.Image}}", r.Container(config.Sandbox))
	if err != nil {
		return err
	}
	tagBefore, err := r.PodmanOutput("image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		return err
	}
	if tagBefore != containerImage {
		failures.Addf("the tag %s names %s, not the container image %s, before the build", tag, tagBefore, containerImage)
	}
	build, err := Step(dir, "build", nil, r.SA("build"))
	if err != nil {
		return err
	}
	if build.Exit != 0 {
		failures.Addf("build exited %d", build.Exit)
	}
	list, err := Step(dir, "list-after-build", nil, r.SA("list"))
	if err != nil {
		return err
	}
	if !strings.Contains(ListState(list.Output(), config.Sandbox), "outdated") {
		failures.Addf("list does not show %s as outdated", config.Sandbox)
	}
	tagAfter, err := r.PodmanOutput("image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		return err
	}
	if tagAfter == containerImage {
		failures.Addf("the tag still names the container image; the update has nothing to replace")
	}
	if err := writePrivateJSON(filepath.Join(dir, "outdated.json"), map[string]string{"tag": tag, "container_image": containerImage, "tag_image_after_build": tagAfter, "utc": utcNow()}); err != nil {
		return err
	}
	if failures.Count() > 0 {
		return fmt.Errorf("make-outdated: %d deviation(s); the update cannot be observed", failures.Count())
	}
	r.Printf("image outdated: container %s, tag %s now %s\n", containerImage, tag, tagAfter)
	return nil
}

// PersistenceUpdate runs the plain `update` once, records the configuration after it and the
// SSH login without a prompt, and evaluates the recorded evidence. Round 2 and the sessions
// follow as the person's steps.
func (r *Run) PersistenceUpdate() error {
	dir, config, err := r.loadPersistence()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "outdated.json")); err != nil {
		return fmt.Errorf("run make-outdated first: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.json")); err == nil {
		return errors.New("the update already ran in this run; use verify-update to evaluate it again")
	}
	update, err := Step(dir, "update", nil, r.SA("update", config.Sandbox))
	if err != nil {
		return err
	}
	after, err := r.inspectConfig(config.Sandbox)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "config-after.txt"), after); err != nil {
		return err
	}
	if _, err := Step(dir, "ssh-true", nil, exec.Command("ssh", "-o", "BatchMode=yes", config.Sandbox+"."+r.Group, "true")); err != nil {
		return err
	}
	_ = update
	return r.PersistenceVerifyUpdate()
}

// PersistenceVerifyUpdate evaluates the recorded update evidence without running anything: the
// update's exit, the image change to the built image, the unchanged limits, port, toolchain set,
// and volume set, and the SSH login. It can be repeated, because the update itself is run once.
func (r *Run) PersistenceVerifyUpdate() error {
	dir, _, err := r.loadPersistence()
	if err != nil {
		return err
	}
	var outdated map[string]string
	if err := readJSON(filepath.Join(dir, "outdated.json"), &outdated); err != nil {
		return fmt.Errorf("run make-outdated first: %w", err)
	}
	var update, ssh StepResult
	if err := readJSON(filepath.Join(dir, "update.json"), &update); err != nil {
		return fmt.Errorf("the update has not run: %w", err)
	}
	if err := readJSON(filepath.Join(dir, "ssh-true.json"), &ssh); err != nil {
		return fmt.Errorf("the SSH check has not run: %w", err)
	}
	failures := &Failures{run: r}
	if update.Exit != 0 {
		failures.Addf("update exited %d", update.Exit)
	}
	beforeData, err := os.ReadFile(filepath.Join(dir, "config-before.txt"))
	if err != nil {
		return err
	}
	afterData, err := os.ReadFile(filepath.Join(dir, "config-after.txt"))
	if err != nil {
		return err
	}
	beforeImage, beforeRest := splitConfig(string(beforeData))
	afterImage, afterRest := splitConfig(string(afterData))
	if afterImage != outdated["tag_image_after_build"] {
		failures.Addf("the container image %s is not the built image %s", afterImage, outdated["tag_image_after_build"])
	}
	if afterImage == beforeImage {
		failures.Addf("the image ID did not change")
	}
	if beforeRest != afterRest {
		failures.Addf("the configuration differs beyond the image ID:\nbefore: %s\nafter: %s", beforeRest, afterRest)
	}
	if ssh.Exit != 0 {
		failures.Addf("ssh true exited %d", ssh.Exit)
	}
	return failures.Finish(dir, "update-complete", fmt.Sprintf("update exit %d; image %s -> %s equals the built image; limits, volumes, port, and toolchain set unchanged; ssh connects without a prompt; round 2 and the sessions remain", update.Exit, beforeImage, afterImage))
}

// splitConfig separates the recorded configuration into the image ID and the rest, with the mount
// names and destinations as a sorted set, because Podman lists mounts in no fixed order.
func splitConfig(text string) (image, rest string) {
	first, second, _ := strings.Cut(strings.TrimRight(text, "\n"), "\n")
	image, limits, _ := strings.Cut(first, " ")
	mounts := strings.Split(strings.TrimSuffix(second, ";"), ";")
	slices.Sort(mounts)
	return image, limits + "\n" + strings.Join(mounts, ";")
}
