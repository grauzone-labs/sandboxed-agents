package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// tmux uses a private server with default session lifetime settings and no user configuration.
func tmux(effects Effects, stdout, stderr io.Writer, args ...string) error {
	var stdin io.Reader
	if len(args) > 0 && args[0] == "attach-session" {
		stdin = effects.Stdin
	}
	return effects.Start(Process{Program: "tmux", Args: append([]string{"-L", "sandboxed-agents", "-f", "/dev/null"}, args...), Identity: agentIdentity, Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// querySessions distinguishes an absent server from errors that cannot rule out sessions.
func querySessions(stdout, stderr io.Writer, effects Effects) error {
	var diagnostic bytes.Buffer
	err := tmux(effects, stdout, &diagnostic, "list-sessions", "-F", "#{session_name}")
	_, connectionMessage, _ := strings.Cut(diagnostic.String(), "error connecting to")
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == 1 && (strings.Contains(diagnostic.String(), "no server running") || strings.Contains(connectionMessage, "(No such file or directory)")) {
		return nil
	}
	fmt.Fprint(stderr, diagnostic.String())
	return err
}

func runningSessions(effects Effects) ([]string, error) {
	var out, diagnostic bytes.Buffer
	if err := querySessions(&out, &diagnostic, effects); err != nil {
		return nil, fmt.Errorf("cannot query agent sessions: %s%w", diagnostic.String(), err)
	}
	return strings.Fields(out.String()), nil
}

func hasSession(names []string, name string) bool { return slices.Contains(names, name) }

func stopAgentSession(name string, stdout io.Writer, effects Effects) error {
	var diagnostic bytes.Buffer
	if err := tmux(effects, io.Discard, &diagnostic, "kill-session", "-t", "="+name); err != nil {
		return fmt.Errorf("cannot stop session %s: %s%w", name, diagnostic.String(), err)
	}
	fmt.Fprintf(stdout, "stopped session %s\n", name)
	return nil
}

// guardAgentSession runs under the manager lock directly before installation changes.
func guardAgentSession(r agentRequest, stdout io.Writer, effects Effects) error {
	names, err := runningSessions(effects)
	if err != nil {
		return err
	}
	if !hasSession(names, r.name) {
		return nil
	}
	if !r.force {
		return fmt.Errorf("agent session %s is running; retry with --force to stop it", r.name)
	}
	return stopAgentSession(r.name, stdout, effects)
}
