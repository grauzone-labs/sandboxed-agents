package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type managerUnavailable struct{ cause error }

func (e managerUnavailable) Error() string { return e.cause.Error() }

func agentManagerError(request Request, cause error) error {
	return fmt.Errorf("sandbox %q: manager does not answer; use check %s for diagnosis and restart %s (%w)", request.Name, request.Name, request.Name, cause)
}

func administerAgent(request Request, p podmanTarget, stdout io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return err
	}
	if err := checkManager(p, names.Container); err != nil {
		return agentManagerError(request, err)
	}
	verb := strings.TrimPrefix(request.Command, "agents ")
	if verb == "status" {
		return printAgentStatus(request, p, stdout)
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
			return fmt.Errorf("%w; to enable the agent use agents enable %s %s", err, request.Name, request.Args[1])
		}
		return err
	}
	return nil
}

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
	if status != 0 {
		return fmt.Errorf("%s: %s", strings.Join(payload[:3], " "), strings.TrimSpace(diagnostic.String()))
	}
	_, err = io.Copy(stdout, &output)
	return err
}

func printAgentStatus(request Request, p podmanTarget, stdout io.Writer) error {
	answer, err := requestManager(request, p, "agents", "status", request.Args[1])
	if err != nil {
		return agentManagerError(request, err)
	}
	var status struct {
		Name             string `json:"name"`
		Enabled          bool   `json:"enabled"`
		InstalledVersion string `json:"installed_version"`
		Pin              string `json:"pin"`
		SignedIn         string `json:"signed_in"`
		Session          bool   `json:"session"`
	}
	if err := json.Unmarshal([]byte(answer), &status); err != nil || status.Name != request.Args[1] || (status.Enabled && status.InstalledVersion == "") {
		return agentManagerError(request, fmt.Errorf("invalid agent status answer"))
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
