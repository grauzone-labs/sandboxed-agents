package manualcheck

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// VSCodePrepare creates the sandbox of item `vscode-remote-ssh` with its SSH setup and proves the
// setup from the host: the host entry exists and an SSH login prints `agent` without a host key
// prompt. The editor steps are the person's and are printed as the checkpoint.
func (r *Run) VSCodePrepare(sandbox string) error {
	if sandbox == "" {
		sandbox = "chk-vs"
	}
	if !namePattern.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	dir, err := r.ItemDir("vscode-remote-ssh")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	up, err := Step(dir, "up", nil, r.SA("up", sandbox, "--ssh-config"))
	if err != nil {
		return err
	}
	if up.Exit != 0 {
		return fmt.Errorf("up %s --ssh-config exited %d; see %s", sandbox, up.Exit, up.Log)
	}
	host := sandbox + "." + r.Group
	entry, err := Step(dir, "ssh-entry", nil, r.SA("ssh-config", sandbox))
	if err != nil {
		return err
	}
	if entry.Exit != 0 || !entry.Contains(host) {
		failures.Addf("ssh-config %s does not print the host entry %s", sandbox, host)
	}
	whoami, err := Step(dir, "ssh-whoami", nil, exec.Command("ssh", "-o", "BatchMode=yes", host, "whoami"))
	if err != nil {
		return err
	}
	if whoami.Exit != 0 || strings.TrimSpace(whoami.Output()) != "agent" {
		failures.Addf("ssh %s whoami did not print agent without a prompt (exit %d)", host, whoami.Exit)
	}
	if err := r.Checkpoint(dir,
		fmt.Sprintf("In VS Code with the Remote - SSH extension, run Remote-SSH: Connect to Host... and choose %s. Expected: the connection opens with no host key prompt.", host),
		"Open the folder /workspace, create a file in the editor, and run `whoami` in the integrated terminal. Expected: `agent`.",
		fmt.Sprintf("Then run `manual-check vscode verify --run %s --file NAME` with the file's name, close the VS Code window, and run `manual-check vscode cleanup --run %s`.", r.ID, r.ID),
	); err != nil {
		return err
	}
	return failures.Finish(dir, "vscode-remote-ssh (preparation)", "sandbox up with the SSH setup; host entry present; ssh whoami prints agent without a prompt; the editor steps are the person's")
}

// VSCodeVerify checks that the file the person created in the editor appears under /workspace
// of the sandbox.
func (r *Run) VSCodeVerify(sandbox, file string) error {
	if sandbox == "" {
		sandbox = "chk-vs"
	}
	if file == "" || file != filepath.Base(file) || strings.ContainsAny(file, "'\"`$\\") {
		return fmt.Errorf("file must be a plain file name")
	}
	dir, err := r.ItemDir("vscode-remote-ssh")
	if err != nil {
		return err
	}
	result, err := r.ShellStep(dir, "verify-file", sandbox, fmt.Sprintf("ls -l '/workspace/%s'", file))
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	if result.Exit != 0 {
		failures.Addf("/workspace/%s is not in the sandbox", file)
	}
	return failures.Finish(dir, "vscode-remote-ssh (verification)", fmt.Sprintf("/workspace/%s created in the editor is present in shell %s", file, sandbox))
}

// VSCodeCleanup removes the SSH setup and the sandbox of the item.
func (r *Run) VSCodeCleanup(sandbox string) error {
	if sandbox == "" {
		sandbox = "chk-vs"
	}
	dir, err := r.ItemDir("vscode-remote-ssh")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	for _, step := range [][]string{{"ssh-config", sandbox, "--remove"}, {"remove", sandbox, "--volumes"}} {
		result, err := Step(dir, "cleanup-"+step[0], nil, r.SA(step...))
		if err != nil {
			return err
		}
		if result.Exit != 0 {
			failures.Addf("%s exited %d", strings.Join(step, " "), result.Exit)
		}
	}
	return failures.Finish(dir, "vscode-remote-ssh (cleanup)", "SSH setup removed and sandbox removed with its volumes")
}
