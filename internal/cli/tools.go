package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// toolManagerArgs builds the Podman arguments that start the manager directly as agent for Tool work (ADR-0009).
func toolManagerArgs(request Request, payload ...string) []string {
	return append([]string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", DerivedNames(request.Group, request.Name).Container, managerPath, "tools"}, payload...)
}

// administerTool enforces the ordered sandbox checks and dispatches a validated Tool operation to the manager as agent.
func administerTool(request Request, p podmanTarget, stdout io.Writer) error {
	if _, err := requireAgentManager(request, p); err != nil {
		return err
	}
	verb := strings.TrimPrefix(request.Command, "tools ")
	if verb == "status" {
		return printToolStatus(request, p, stdout)
	}
	if verb == "update" {
		status, err := readToolStatus(request, p)
		if err != nil {
			return err
		}
		if !status.Enabled {
			return fmt.Errorf("%s not enabled; run tools enable %s %s", request.Args[1], request.Name, request.Args[1])
		}
	}
	payload := []string{verb, request.Args[1]}
	for _, option := range []string{"channel", "version"} {
		if value := request.Options[option]; value != "" {
			payload = append(payload, "--"+option, value)
		}
	}
	var output, diagnostic bytes.Buffer
	exit, err := p.run(nil, &output, &diagnostic, toolManagerArgs(request, payload...)...)
	if err != nil || podmanTransportFailure(exit) {
		if err == nil {
			err = fmt.Errorf("manager request failed with exit status %d: %s", exit, strings.TrimSpace(diagnostic.String()))
		}
		return agentManagerError(request, err)
	}
	_, outputErr := io.Copy(stdout, &output)
	if exit != 0 {
		return errors.Join(fmt.Errorf("tools %s %s: %s", verb, request.Args[1], strings.TrimSpace(diagnostic.String())), outputErr)
	}
	return outputErr
}

// readToolStatus asks the manager, as agent, for the Tool selection and validates the answer before callers use it.
func readToolStatus(request Request, p podmanTarget) (catalog.ToolStatus, error) {
	var status catalog.ToolStatus
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answer, err := p.queryContext(ctx, time.Second, toolManagerArgs(request, "status", request.Args[1])...)
	if err != nil {
		if ctx.Err() == nil && managerOperationFailure(err) {
			return status, err
		}
		return status, agentManagerError(request, errors.Join(err, ctx.Err()))
	}
	if err := json.Unmarshal([]byte(answer), &status); err != nil || status.Name != request.Args[1] || (status.Enabled && (status.InstalledVersion == "" || (status.Channel == "") == (status.Pin == ""))) {
		return status, agentManagerError(request, fmt.Errorf("invalid tool status answer"))
	}
	return status, nil
}

// printToolStatus renders the installed version and the channel or pin, or an absent selection.
func printToolStatus(request Request, p podmanTarget, stdout io.Writer) error {
	status, err := readToolStatus(request, p)
	if err != nil {
		return err
	}
	if !status.Enabled {
		_, err = fmt.Fprintf(stdout, "%s: not enabled\n", status.Name)
		return err
	}
	channel, pin := status.Channel, status.Pin
	if channel == "" {
		channel = "none"
	}
	if pin == "" {
		pin = "none"
	}
	_, err = fmt.Fprintf(stdout, "%s: enabled; version %s; channel %s; pin %s\n", status.Name, status.InstalledVersion, channel, pin)
	return err
}
