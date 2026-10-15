package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// runAgentWork checks sandbox and selection state before starting unprivileged agent work.
func runAgentWork(request Request, p podmanTarget, stdout, stderr io.Writer) (int, error) {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return 1, err
	}
	names, err := requireRunning(request, objects)
	if err != nil {
		return 1, err
	}
	if err := checkManager(p, names.Container); err != nil {
		return 1, agentManagerError(request, err)
	}
	status, err := readAgentStatus(request, p)
	if err != nil {
		return 1, err
	}
	stopping := request.Command == "agents session" && request.Options["stop"] != ""
	if !status.Enabled && !stopping {
		return 1, fmt.Errorf("%s not enabled; run agents enable %s %s", request.Args[1], request.Name, request.Args[1])
	}
	args := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", "-i"}
	interactive := p.host.IsInputTerminal(p.host.Stdin) && p.host.IsOutputTerminal(stdout)
	if request.Command != "agents run" && !stopping && !interactive {
		return 1, fmt.Errorf("%s %s needs an interactive terminal", request.Args[1], strings.TrimPrefix(request.Command, "agents "))
	}
	if interactive && !stopping {
		args = append(args, "-t")
	}
	args = append(args, names.Container, managerPath, "agents", strings.TrimPrefix(request.Command, "agents "))
	if request.Command == "agents login" {
		workflow, err := catalog.Default.ResolveAgentLogin(request.Args[1], loginWorkflow(request.Args))
		if err != nil {
			return 1, err
		}
		args = append(args, request.Args[1], workflow)
	} else {
		args = append(args, request.Args[1:]...)
		if stopping {
			args = append(args, "--stop")
		}
	}
	exit, err := p.run(p.host.Stdin, stdout, stderr, args...)
	if request.Command == "agents run" {
		return exit, err
	}
	if err != nil {
		return 1, err
	}
	if exit != 0 {
		return 1, fmt.Errorf("%s failed", request.Command)
	}
	return 0, nil
}
