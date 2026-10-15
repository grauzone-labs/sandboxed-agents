package manager

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// agentWork keeps one verified agent request and its home and process effects together.
type agentWork struct {
	request        agentRequest
	home, root     string
	stdout, stderr io.Writer
	effects        Effects
}

// executeAgent runs work only under the already unprivileged identity.
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
	work := agentWork{request: r, home: home, root: filepath.Join(home, ".local", "share", "sandboxed-agents"), stdout: stdout, stderr: stderr, effects: effects}
	enabled, err := work.requireEnabled()
	if err != nil {
		return exitStatus(err, stderr)
	}
	if !enabled {
		return 0
	}
	if r.operation == "session" {
		return work.session()
	}
	if r.operation == "login" {
		fmt.Fprintln(stdout, r.definition.LoginMessage)
	}
	err = effects.Start(Process{Program: commandPath(home, r.definition), Args: r.arguments, Directory: "/workspace", Identity: agentIdentity, Stdin: effects.Stdin, Stdout: stdout, Stderr: stderr})
	if err == nil {
		return 0
	}
	if r.operation == "run" {
		var child interface{ ExitCode() int }
		if errors.As(err, &child) && child.ExitCode() > 0 {
			return child.ExitCode()
		}
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// requireEnabled reads selection afresh, also when called again under the manager lock.
func (w agentWork) requireEnabled() (bool, error) {
	selection, err := readSelection(w.root)
	if err != nil {
		return false, err
	}
	if _, enabled := selection[w.request.name]; enabled {
		return true, nil
	}
	if w.request.stop {
		fmt.Fprintf(w.stdout, "%s not enabled; no session to stop\n", w.request.name)
		return false, nil
	}
	return false, fmt.Errorf("%s not enabled; run agents enable NAME %s", w.request.name, w.request.name)
}

// session attaches without a lock; creation and stopping share the manager lock.
func (w agentWork) session() int {
	if !w.request.stop {
		names, err := readSessionNames(w.effects)
		if err != nil {
			return exitStatus(err, w.stderr)
		}
		if slices.Contains(names, w.request.name) {
			return w.attach()
		}
	}
	if err := os.MkdirAll(w.root, 0700); err != nil {
		return exitStatus(err, w.stderr)
	}
	unlock, err := lockManager(filepath.Join(w.root, "manager.lock"))
	if err != nil {
		return exitStatus(err, w.stderr)
	}
	err = w.changeSession()
	unlock()
	if err != nil {
		return exitStatus(err, w.stderr)
	}
	if w.request.stop {
		return 0
	}
	return w.attach()
}

// changeSession rechecks selection and sessions under the manager lock before mutation.
func (w agentWork) changeSession() error {
	enabled, err := w.requireEnabled()
	if err != nil || !enabled {
		return err
	}
	names, err := readSessionNames(w.effects)
	if err != nil {
		return err
	}
	running := slices.Contains(names, w.request.name)
	if w.request.stop {
		if !running {
			fmt.Fprintf(w.stdout, "%s has no running session; nothing to stop\n", w.request.name)
			return nil
		}
		return stopAgentSession(w.request.name, w.stdout, w.effects)
	}
	if running {
		return nil
	}
	return tmux(w.effects, io.Discard, w.stderr, "new-session", "-d", "-s", w.request.name, "-c", "/workspace", "--", "/usr/bin/env", commandPath(w.home, w.request.definition))
}

// attach selects the exact session name and keeps terminal IO outside the manager lock.
func (w agentWork) attach() int {
	return exitStatus(tmux(w.effects, w.stdout, w.stderr, "attach-session", "-t", "="+w.request.name), w.stderr)
}
