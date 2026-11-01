package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/grauzone-labs/sandboxed-agents/internal/azurebrowser"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// An installed alias is a sandbox name plus an optional controller-group suffix.
// Both fit the host CLI's sandbox-name alphabet; recheck at the manager boundary
// before composing the BROWSER command or printable SSH command.
var sshAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// loginAzure verifies agent identity before signing in and checking the selected account.
func loginAzure(workflow string, options []string, stdout, stderr io.Writer, effects Effects) int {
	sshHost := ""
	login := []string{"login", "--use-device-code"}
	if workflow == catalog.BrowserWorkflow {
		if len(options) != 2 || options[0] != "--ssh-host" || !sshAliasPattern.MatchString(options[1]) {
			fmt.Fprintln(stderr, "azure browser requires --ssh-host with an installed SSH host alias")
			return 2
		}
		sshHost, login = options[1], []string{"login"}
	} else if len(options) != 0 {
		fmt.Fprintln(stderr, "azure device-code takes no options")
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	for _, step := range [][]string{login, {"account", "show"}} {
		if err := effects.Start(Process{Program: "az", Args: step, Identity: agentIdentity, EnvironmentPolicy: AzureEnvironment, AzureSSHHost: sshHost, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

// azureBrowser is the synchronous BROWSER callback: MSAL invokes it once the
// listener's actual port is known, before waiting for the authorization reply.
func azureBrowser(sshHost, address string, stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	if !sshAliasPattern.MatchString(sshHost) {
		fmt.Fprintln(stderr, "invalid Azure browser callback")
		return 1
	}
	if _, err := azurebrowser.Port(address); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	data, _ := json.Marshal(address)
	_, err := fmt.Fprintf(stdout, "%s%s\n", azurebrowser.Prefix, data)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
