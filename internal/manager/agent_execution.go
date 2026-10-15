package manager

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// executeAgent runs interactive work only under the already unprivileged identity.
func executeAgent(r agentRequest, stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	if effects.HomeDirectory == nil {
		return exitStatus(fmt.Errorf("agent home storage is unavailable"), stderr)
	}
	home, err := effects.HomeDirectory()
	if err != nil {
		return exitStatus(err, stderr)
	}
	root := filepath.Join(home, ".local", "share", "sandboxed-agents")
	selection, err := readSelection(root)
	if err != nil {
		return exitStatus(err, stderr)
	}
	if r.operation == "session" {
		return agentSession(r, home, root, selection, stdout, stderr, effects)
	}
	if _, enabled := selection[r.name]; !enabled {
		return exitStatus(fmt.Errorf("%s not enabled; run agents enable NAME %s", r.name, r.name), stderr)
	}
	if r.operation == "login" {
		fmt.Fprintln(stdout, r.definition.LoginMessage)
	}
	err = effects.Start(Process{Program: commandPath(home, r.definition), Args: r.arguments, Directory: "/workspace", Identity: agentIdentity, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr})
	if r.operation == "login" && err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return exitStatus(err, stderr)
}

// agentSession attaches without a lock, while creation and stopping share the installation lock.
func agentSession(r agentRequest, home, root string, selection map[string]installation, stdout, stderr io.Writer, effects Effects) int {
	if _, enabled := selection[r.name]; !enabled {
		if r.stop {
			fmt.Fprintf(stdout, "%s not enabled; no session to stop\n", r.name)
			return 0
		}
		return exitStatus(fmt.Errorf("%s not enabled; run agents enable NAME %s", r.name, r.name), stderr)
	}
	if !r.stop {
		names, err := runningSessions(effects)
		if err != nil {
			return exitStatus(err, stderr)
		}
		if hasSession(names, r.name) {
			return attachAgent(r.name, stdout, stderr, effects)
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return exitStatus(err, stderr)
	}
	unlock, err := lockManager(filepath.Join(root, "manager.lock"))
	if err != nil {
		return exitStatus(err, stderr)
	}
	// Installation may have changed between the initial read and taking the lock.
	err = changeSession(r, home, root, stdout, stderr, effects)
	unlock()
	if err != nil {
		return exitStatus(err, stderr)
	}
	if r.stop {
		return 0
	}
	return attachAgent(r.name, stdout, stderr, effects)
}

func changeSession(r agentRequest, home, root string, stdout, stderr io.Writer, effects Effects) error {
	selection, err := readSelection(root)
	if err != nil {
		return err
	}
	if _, enabled := selection[r.name]; !enabled {
		if r.stop {
			fmt.Fprintf(stdout, "%s not enabled; no session to stop\n", r.name)
			return nil
		}
		return fmt.Errorf("%s not enabled; run agents enable NAME %s", r.name, r.name)
	}
	names, err := runningSessions(effects)
	if err != nil {
		return err
	}
	if r.stop {
		if !hasSession(names, r.name) {
			fmt.Fprintf(stdout, "%s has no running session; nothing to stop\n", r.name)
			return nil
		}
		return stopAgentSession(r.name, stdout, effects)
	}
	if hasSession(names, r.name) {
		return nil
	}
	return tmux(effects, io.Discard, stderr, "new-session", "-d", "-s", r.name, "-c", "/workspace", "--", "/usr/bin/env", commandPath(home, r.definition))
}

func attachAgent(name string, stdout, stderr io.Writer, effects Effects) int {
	return exitStatus(tmux(effects, stdout, stderr, "attach-session", "-t", "="+name), stderr)
}
