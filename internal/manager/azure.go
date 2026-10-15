package manager

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

var sshAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// loginAzure verifies agent identity before signing in and checking the selected account.
func loginAzure(args []string, stdout, stderr io.Writer, effects Effects) int {
	workflow := ""
	if len(args) > 0 {
		workflow = args[0]
	}
	if _, err := catalog.Default.ResolveLogin("azure", workflow); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sshHost := ""
	login := []string{"login", "--use-device-code"}
	if workflow == "browser" {
		if len(args) != 3 || args[1] != "--ssh-host" || !sshAliasPattern.MatchString(args[2]) {
			fmt.Fprintln(stderr, "azure browser requires --ssh-host with an installed SSH host alias")
			return 2
		}
		sshHost, login = args[2], []string{"login"}
	} else if len(args) != 1 {
		fmt.Fprintln(stderr, "azure device-code takes no options")
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	for _, args := range [][]string{login, {"account", "show"}} {
		if err := effects.Start(Process{Program: "az", Args: args, Identity: agentIdentity, EnvironmentPolicy: AzureEnvironment, AzureSSHHost: sshHost, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr}); err != nil {
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
	u, err := url.Parse(address)
	if err != nil || !sshAliasPattern.MatchString(sshHost) || strings.ContainsAny(address, "\r\n\t\x1b") || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		fmt.Fprintln(stderr, "invalid Azure browser callback")
		return 1
	}
	redirect := u
	if u.Scheme == "https" {
		redirect, err = url.Parse(u.Query().Get("redirect_uri"))
	}
	if err != nil || redirect.Scheme != "http" || (redirect.Hostname() != "localhost" && redirect.Hostname() != "127.0.0.1") || redirect.User != nil {
		fmt.Fprintln(stderr, "Azure browser callback has no loopback redirect; cancel login and retry with device-code")
		return 1
	}
	port, err := strconv.Atoi(redirect.Port())
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintln(stderr, "Azure browser callback has an invalid redirect port")
		return 1
	}
	_, err = fmt.Fprintf(stdout, "On the machine with your browser, run this in another terminal and leave it running:\nssh -N -L 127.0.0.1:%d:127.0.0.1:%d -o ExitOnForwardFailure=yes %s\nThen open this URL in that browser:\n%s\n", port, port, sshHost, address)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
