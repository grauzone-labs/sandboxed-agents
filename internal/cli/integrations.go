package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

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
	args := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent"}
	var stdin io.Reader
	if request.Command == "integrations login" {
		if !p.host.IsInputTerminal(p.host.Stdin) || !p.host.IsOutputTerminal(stdout) {
			return fmt.Errorf("%s login needs an interactive terminal", request.Args[1])
		}
		workflow, err := catalog.Default.ResolveLogin(request.Args[1], loginWorkflow(request.Args))
		if err != nil {
			return err
		}
		args = append(args, "-it", names.Container, managerPath, "integrations", "login", request.Args[1], workflow)
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
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("integration workflow failed")
	}
	return nil
}

type gitIdentity struct{ Name, Email string }

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

func loginWorkflow(args []string) string {
	if len(args) == 3 {
		return args[2]
	}
	return ""
}
