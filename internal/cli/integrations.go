package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/azdo"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// runIntegration checks sandbox readiness, runs the selected workflow as agent, and normalizes workflow failures.
func runIntegration(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return err
	}
	if _, err := queryManagerSessions(request, p); err != nil {
		return fmt.Errorf("sandbox %q: manager does not answer; use check %s for diagnosis and restart %s (%w)", request.Name, request.Name, request.Name, err)
	}
	azdoLogin := request.Command == "integrations login" && request.Args[1] == catalog.AzureDevOpsIntegration
	args := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent"}
	var stdin io.Reader
	if azdoLogin {
		token, err := readAzureDevOpsToken(p.host, stderr)
		if err != nil {
			return err
		}
		args = append(args, "-i", names.Container, managerPath, "integrations", "login", catalog.AzureDevOpsIntegration, "--"+catalog.OrganizationOption, request.OrganizationURL)
		stdin = strings.NewReader(token + "\n")
		// Credential subprocess diagnostics are untrusted and may contain the PAT.
		stdout, stderr = io.Discard, io.Discard
	} else if request.Command == "integrations login" {
		if request.Args[1] == catalog.AzureIntegration {
			set, err := sandboxToolchains(objects.container)
			if err != nil {
				return err
			}
			if !slices.Contains(set, "azure") {
				set = append(set, "azure")
				slices.Sort(set)
				return fmt.Errorf("azure login requires the azure toolchain; run update %s --with %s", request.Name, toolchainSet(set))
			}
		}
		browser := request.Args[1] == catalog.AzureIntegration && loginWorkflow(request.Args) == catalog.BrowserWorkflow
		if browser {
			setup, err := sshSetupPaths(request, p.host.Platform)
			if err != nil {
				return err
			}
			if _, err := os.Stat(setup.entry); err != nil {
				return fmt.Errorf("Azure browser login needs an installed SSH setup; run ssh-config %s --install (%w)", request.Name, err)
			}
		}
		if !p.host.IsInputTerminal(p.host.Stdin) || !p.host.IsOutputTerminal(stdout) {
			return fmt.Errorf("%s login needs an interactive terminal", request.Args[1])
		}
		workflow, err := catalog.Default.ResolveLogin(request.Args[1], loginWorkflow(request.Args))
		if err != nil {
			return err
		}
		args = append(args, "-it", names.Container, managerPath, "integrations", "login", request.Args[1], workflow)
		if browser {
			args = append(args, "--ssh-host", sshHostName(request))
		}
		stdin = p.host.Stdin
	} else {
		selection := request.ConfigSelection
		args = append(args, names.Container, managerPath, "integrations", "config", selection.Integration, selection.Workflow)
		if selection.Integration == catalog.GitIntegration && selection.Workflow == catalog.GitIdentityWorkflow {
			identity, err := readGitIdentity(request.Options[catalog.GitNameOption], request.Options[catalog.GitEmailOption], p.host, stdout)
			if err != nil {
				return err
			}
			args = append(args, "--"+catalog.GitNameOption, identity.Name, "--"+catalog.GitEmailOption, identity.Email)
		}
	}
	status, err := p.run(stdin, stdout, stderr, args...)
	if azdoLogin && (err != nil || status != 0) {
		return fmt.Errorf("Azure DevOps login failed; credentials may be partially configured")
	}
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("integration workflow failed")
	}
	return nil
}

type gitIdentity struct{ Name, Email string }

// readGitIdentity collects explicit or terminal-supplied name and email before any Git configuration is written.
func readGitIdentity(name, email string, host Host, stdout io.Writer) (gitIdentity, error) {
	identity := gitIdentity{Name: name, Email: email}
	if name != "" && email != "" {
		return identity, nil
	}
	if !host.IsInputTerminal(host.Stdin) || !host.IsOutputTerminal(stdout) {
		return identity, fmt.Errorf("git identity needs a terminal for missing --name or --email; supply both options")
	}
	reader := bufio.NewReader(host.Stdin)
	for _, field := range []struct {
		name, prompt string
		value        *string
	}{{catalog.GitNameOption, "Name: ", &identity.Name}, {catalog.GitEmailOption, "Email: ", &identity.Email}} {
		if *field.value != "" {
			continue
		}
		if _, err := fmt.Fprint(stdout, field.prompt); err != nil {
			return identity, err
		}
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return identity, fmt.Errorf("read Git %s: %w", field.name, err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return identity, fmt.Errorf("Git %s is required; no configuration changed", field.name)
		}
		*field.value = value
	}
	return identity, nil
}

// loginWorkflow returns the optional workflow from validated host arguments, leaving an omitted workflow empty.
func loginWorkflow(args []string) string {
	if len(args) == 3 {
		return args[2]
	}
	return ""
}

// readAzureDevOpsToken selects hidden terminal input or the shared first-line reader using stdin alone.
func readAzureDevOpsToken(host Host, stderr io.Writer) (string, error) {
	if host.IsInputTerminal(host.Stdin) {
		data, err := readTerminalSecret(host.Stdin, stderr)
		if err != nil {
			return "", fmt.Errorf("cannot read Azure DevOps token")
		}
		return azdo.NormalizeToken(string(data))
	}
	return azdo.ReadToken(host.Stdin)
}
