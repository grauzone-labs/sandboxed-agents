package manager

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// loginAzureDevOps stores the PAT through stdin-only CLI interfaces after verifying agent identity.
func loginAzureDevOps(args []string, stderr io.Writer, effects Effects) int {
	if len(args) != 2 || args[0] != "--organization" {
		fmt.Fprintln(stderr, "azdo requires --organization URL and no workflow")
		return 2
	}
	organization, username, err := catalog.AzureDevOpsOrganization(args[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	if effects.Stdin == nil {
		fmt.Fprintln(stderr, "Azure DevOps token is required")
		return 1
	}
	token, err := bufio.NewReader(effects.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintln(stderr, "cannot read Azure DevOps token")
		return 1
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		fmt.Fprintln(stderr, "Azure DevOps token is required and must be a single line")
		return 1
	}
	processes := []Process{
		{Program: "az", Args: []string{"devops", "login", "--organization", organization, "--only-show-errors"}, Stdin: strings.NewReader(token + "\n")},
		{Program: "git", Args: []string{"config", "--global", "--replace-all", "--", "credential.helper", "store --file=/home/agent/.git-credentials"}},
		{Program: "git", Args: []string{"config", "--global", "--replace-all", "--", "credential." + organization + ".username", username}},
		{Program: "git", Args: []string{"-c", "credential.helper=", "-c", "credential.helper=store --file=/home/agent/.git-credentials", "-c", "credential.useHttpPath=false", "credential", "approve"}, Stdin: strings.NewReader("url=" + organization + "\nusername=" + username + "\npassword=" + token + "\n\n")},
	}
	for _, p := range processes {
		p.Identity = agentIdentity
		p.EnvironmentPolicy = AzureDevOpsEnvironment
		// Child diagnostics and errors may contain credentials. Never forward them.
		p.Stdout, p.Stderr = io.Discard, io.Discard
		if err := effects.Start(p); err != nil {
			fmt.Fprintf(stderr, "Azure DevOps login failed while running %s; credentials may be partially configured\n", p.Program)
			return 1
		}
	}
	return 0
}
