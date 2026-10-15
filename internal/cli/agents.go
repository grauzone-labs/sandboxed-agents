package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

type managerUnavailable struct{ cause error }

// Error preserves the underlying manager transport failure for the sandbox diagnostic.
func (e managerUnavailable) Error() string { return e.cause.Error() }

// agentManagerError names the sandbox diagnosis and restart commands for an unusable manager answer.
func agentManagerError(request Request, cause error) error {
	return fmt.Errorf("sandbox %q: manager does not answer; use check %s for diagnosis and restart %s (%w)", request.Name, request.Name, request.Name, cause)
}

// agentNotEnabledError gives the host command that enables the requested agent.
func agentNotEnabledError(request Request) error {
	return fmt.Errorf("%s not enabled; run agents enable %s %s", request.Args[1], request.Name, request.Args[1])
}

// requireAgentManager checks sandbox existence, ownership, running state, and manager availability in order.
func requireAgentManager(request Request, p podmanTarget) (ObjectNames, error) {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return ObjectNames{}, err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return ObjectNames{}, err
	}
	if err := checkManager(p, names.Container); err != nil {
		return ObjectNames{}, agentManagerError(request, err)
	}
	return names, nil
}

// administerAgent enforces ordered sandbox checks and dispatches a validated agent operation through the manager.
func administerAgent(request Request, p podmanTarget, stdout io.Writer) error {
	if _, err := requireAgentManager(request, p); err != nil {
		return err
	}
	verb := strings.TrimPrefix(request.Command, "agents ")
	if verb == "status" {
		return printAgentStatus(request, p, stdout)
	}
	if verb == "update" {
		status, err := readAgentStatus(request, p)
		if err != nil {
			return err
		}
		if !status.Enabled {
			return agentNotEnabledError(request)
		}
	}
	args := []string{"agents", verb, request.Args[1]}
	for _, option := range []string{"version", "unpin", "force"} {
		if value := request.Options[option]; value != "" {
			args = append(args, "--"+option)
			if option == "version" {
				args = append(args, value)
			}
		}
	}
	if err := runAgentChange(request, p, stdout, args...); err != nil {
		var unavailable managerUnavailable
		if errors.As(err, &unavailable) {
			return agentManagerError(request, err)
		}
		if verb == "update" {
			retry := "agents update " + request.Name + " " + request.Args[1]
			for _, option := range []string{"unpin", "force"} {
				if request.Options[option] != "" {
					retry += " --" + option
				}
			}
			return fmt.Errorf("%w; retry: %s", err, retry)
		}
		return err
	}
	return nil
}

// runAgentChange captures an administrative mutation result and separates transport failures from operation failures.
func runAgentChange(request Request, p podmanTarget, stdout io.Writer, payload ...string) error {
	args := append([]string{"exec", "--user=0:0", DerivedNames(request.Group, request.Name).Container, managerPath}, payload...)
	var output, diagnostic bytes.Buffer
	status, err := p.run(nil, &output, &diagnostic, args...)
	if err != nil || status == 125 || status == 126 || status == 127 {
		if err == nil {
			err = fmt.Errorf("manager request failed with exit status %d: %s", status, strings.TrimSpace(diagnostic.String()))
		}
		return managerUnavailable{cause: err}
	}
	_, outputErr := io.Copy(stdout, &output)
	if status != 0 {
		return errors.Join(fmt.Errorf("%s: %s", strings.Join(payload[:3], " "), strings.TrimSpace(diagnostic.String())), outputErr)
	}
	return outputErr
}

// readAgentStatus validates the shared manager response before callers use its selection state.
func readAgentStatus(request Request, p podmanTarget) (catalog.AgentStatus, error) {
	var status catalog.AgentStatus
	answer, err := requestManager(request, p, "agents", "status", request.Args[1])
	if err != nil {
		var exit *exec.ExitError
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && errors.As(err, &exit) {
			code := exit.ExitCode()
			if code > 0 && code != 125 && code != 126 && code != 127 {
				return status, err
			}
		}
		return status, agentManagerError(request, err)
	}
	if err := json.Unmarshal([]byte(answer), &status); err != nil || status.Name != request.Args[1] || (status.Enabled && status.InstalledVersion == "") {
		return status, agentManagerError(request, fmt.Errorf("invalid agent status answer"))
	}
	return status, nil
}

// printAgentStatus renders the recorded version, pin, session, and sign-in state, or an absent selection.
func printAgentStatus(request Request, p podmanTarget, stdout io.Writer) error {
	status, err := readAgentStatus(request, p)
	if err != nil {
		return err
	}
	if !status.Enabled {
		_, err = fmt.Fprintf(stdout, "%s: not enabled\n", status.Name)
		return err
	}
	pin := status.Pin
	if pin == "" {
		pin = "none"
	}
	_, err = fmt.Fprintf(stdout, "%s: enabled; version %s; pin %s; session %t; signed in %s\n", status.Name, status.InstalledVersion, pin, status.Session, status.SignedIn)
	return err
}

// enableUpAgents attempts every requested install after startup and reports retries while preserving the sandbox.
func enableUpAgents(request Request, p podmanTarget, stdout io.Writer) error {
	if request.Options["agents"] == "" {
		return nil
	}
	agents := strings.Split(request.Options["agents"], ",")
	retries := func(names []string) string {
		commands := make([]string, 0, len(names))
		for _, name := range names {
			commands = append(commands, "agents enable "+request.Name+" "+name)
		}
		return strings.Join(commands, "; ")
	}
	if err := checkManager(p, DerivedNames(request.Group, request.Name).Container); err != nil {
		return fmt.Errorf("%w; retry: %s", agentManagerError(request, err), retries(agents))
	}
	var failed []string
	var failures []error
	var installed bytes.Buffer
	for _, name := range agents {
		err := runAgentChange(request, p, &installed, "agents", "enable", name)
		if err == nil {
			continue
		}
		var unavailable managerUnavailable
		if errors.As(err, &unavailable) {
			return fmt.Errorf("%w; retry: %s", agentManagerError(request, err), retries(agents))
		}
		failed = append(failed, name)
		failures = append(failures, err)
	}
	if _, err := io.Copy(stdout, &installed); err != nil {
		return err
	}
	if len(failed) != 0 {
		return fmt.Errorf("failed agents: %s; retry: %s (%v)", strings.Join(failed, ", "), retries(failed), errors.Join(failures...))
	}
	return nil
}
