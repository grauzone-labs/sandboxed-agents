package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

func configureIntegration(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	objects, err := inspectSandbox(request, p)
	if err != nil {
		return err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return err
	}
	args, err := ManagerQueryArgs(names.Container, "/usr/local/bin/sandboxed-agents-manager", "sessions")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.queryContext(ctx, time.Second, args...); err != nil {
		return fmt.Errorf("sandbox %q: manager does not answer; use check %s for diagnosis and restart %s (%w)", request.Name, request.Name, request.Name, err)
	}
	args = []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", names.Container, "/usr/local/bin/sandboxed-agents-manager", "integrations", "config", request.Args[1], request.Args[2]}
	if request.Args[2] == "identity" {
		if err := readGitIdentity(request.Options, p.host, stdout); err != nil {
			return err
		}
		args = append(args, "--name", request.Options["name"], "--email", request.Options["email"])
	}

	status, err := p.run(nil, stdout, stderr, args...)
	if err != nil {
		return err
	}
	if status != 0 {
		return fmt.Errorf("integration workflow failed")
	}
	return nil
}

func readGitIdentity(options map[string]string, host Host, stdout io.Writer) error {
	if options["name"] != "" && options["email"] != "" {
		return nil
	}
	if !host.IsInputTerminal(host.Stdin) || !host.IsOutputTerminal(stdout) {
		return fmt.Errorf("git identity needs a terminal for missing --name or --email; supply both options")
	}
	reader := bufio.NewReader(host.Stdin)
	for _, field := range []struct{ key, prompt string }{{"name", "Name: "}, {"email", "Email: "}} {
		if options[field.key] != "" {
			continue
		}
		if _, err := fmt.Fprint(stdout, field.prompt); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read Git %s: %w", field.key, err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return fmt.Errorf("Git %s is required; no configuration changed", field.key)
		}
		options[field.key] = value
	}
	return nil
}
