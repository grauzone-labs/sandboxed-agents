package manualcheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FinalCleanup runs the mechanical parts of the final cleanup: it removes the SSH setup and the
// shared sandbox with its volumes, creates the sandbox again to observe the removal half of
// `credentials-persist`, removes it for good, takes the images-after inventory and compares it
// with images-before, and checks that no sandbox or container of the group remains. The
// revocations at the providers are the person's step.
func (r *Run) FinalCleanup(sandbox string) error {
	if sandbox == "" {
		sandbox = "chk-persist"
	}
	if !namePattern.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	dir, err := r.ItemDir("final-cleanup")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	run := func(name string, args ...string) StepResult {
		result, err := Step(dir, name, nil, r.SA(args...))
		if err != nil {
			failures.Addf("%s: %v", name, err)
			return result
		}
		if result.Exit != 0 {
			failures.Addf("%s exited %d", strings.Join(args, " "), result.Exit)
		}
		return result
	}
	// 1 and 2. The SSH setup and the sandbox with its volumes.
	run("ssh-config-remove", "ssh-config", sandbox, "--remove")
	run("remove", "remove", sandbox, "--volumes")
	volumes, err := r.PodmanOutput("volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	for _, volume := range strings.Fields(volumes) {
		if strings.Contains(volume, "."+r.Group+"."+sandbox) {
			failures.Addf("volume %s remains after the removal", volume)
		}
	}
	// 3. The removal half of credentials-persist, observed inside a fresh sandbox.
	run("up-again", "up", sandbox, "--with", "azure")
	inside, err := r.ShellStep(dir, "removal-observation", sandbox, `test -e /home/agent/.git-credentials && echo "credentials-file present" || echo "credentials-file absent"
echo "user-email [$(git config --global --get user.email)]"
`+ghStatusProbe+`
`+azAccountProbe)
	if err != nil {
		return err
	}
	if !inside.HasLine("credentials-file absent") {
		failures.Addf("/home/agent/.git-credentials is present in the fresh sandbox")
	}
	if !inside.HasLine("user-email []") {
		failures.Addf("git user.email is still configured in the fresh sandbox")
	}
	if inside.Contains("Logged in to github.com") || inside.HasLine("gh exit 0") {
		failures.Addf("gh auth status still reports a login")
	}
	if inside.HasLine("az exit 0") {
		failures.Addf("az account show still reports a login")
	}
	status := run("status-opencode", "agents", "status", sandbox, "opencode")
	if !status.HasLine("opencode: not enabled") {
		failures.Addf("opencode is still enabled in the fresh sandbox")
	}
	run("remove-final", "remove", sandbox, "--volumes")
	// 4. The image comparison.
	if _, err := r.ImagesInventory("images-after"); err != nil {
		return err
	}
	if err := r.ImagesCompare("images-before", "images-after"); err != nil {
		failures.Addf("%v", err)
	}
	// 6. Nothing of the group remains.
	list := run("list", "list")
	if names := ListNames(list.Output()); len(names) != 0 {
		failures.Addf("list still shows %v", names)
	}
	containers, err := r.PodmanOutput("ps", "--all", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	for _, container := range strings.Fields(containers) {
		if strings.HasPrefix(container, "sandboxed-agents."+r.Group+".") || strings.HasPrefix(container, "sandboxed-agents-backup."+r.Group+".") {
			failures.Addf("container %s remains", container)
		}
	}
	if err := r.Checkpoint(dir,
		"Revoke what you granted: the GitHub device authorization, the Azure sign-in sessions of the test account, the Azure DevOps personal access token, and the four agents' sessions at each provider.",
		fmt.Sprintf("Record the observations of this run with `manual-check record add`; the verdicts are under %s.", filepath.Dir(dir)),
	); err != nil {
		return err
	}
	return failures.Finish(dir, "final-cleanup", "SSH setup and sandbox removed with volumes; fresh sandbox shows no credentials, identity, login, or agent; images-before present in images-after; nothing of the group remains; revocations are the person's step")
}
