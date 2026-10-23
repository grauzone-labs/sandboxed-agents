package cli

import (
	"fmt"
	"io"
)

// runAgentWork checks sandbox and selection state before starting unprivileged agent work.
func runAgentWork(request Request, p podmanTarget, stdout, stderr io.Writer) (int, error) {
	var verb string
	var needsTerminal, stopping, passExit bool
	var payload []string
	switch request.Command {
	case "agents run":
		verb, passExit = "run", true
		payload = request.Args[1:]
	case "agents login":
		verb, needsTerminal = "login", true
		payload = []string{request.Args[1], request.AgentWorkflow}
	case "agents session":
		verb = "session"
		stopping = request.Options["stop"] != ""
		needsTerminal = !stopping
		payload = []string{request.Args[1]}
		if stopping {
			payload = append(payload, "--stop")
		}
	default:
		return 1, fmt.Errorf("unknown agent work command %q", request.Command)
	}
	names, err := requireAgentManager(request, p)
	if err != nil {
		return 1, err
	}
	// The shared status contract provides enabled state and may also run an optional sign-in probe.
	status, err := readAgentStatus(request, p)
	if err != nil {
		return 1, err
	}
	if !status.Enabled && !stopping {
		return 1, agentNotEnabledError(request)
	}
	interactive := !stopping && p.host.IsInputTerminal(p.host.Stdin) && p.host.IsOutputTerminal(stdout)
	if needsTerminal && !interactive {
		return 1, fmt.Errorf("%s %s needs an interactive terminal", request.Args[1], verb)
	}
	exit, err := p.run(p.host.Stdin, stdout, stderr, agentExecArgs(interactive, names.Container, append([]string{"agents", verb}, payload...)...)...)
	if passExit {
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
