// Package manager owns administrative SSH requests, integration workflows, and agent session queries inside a sandbox.
package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

var (
	agentIdentity = Identity{UID: 1000, GID: 1000}
	rootIdentity  = Identity{UID: 0, GID: 0}
)

// Identity is the effective user and group of a process.
type Identity struct{ UID, GID int }

// EnvironmentPolicy selects sandbox environment constraints for a process.
type EnvironmentPolicy uint8

const (
	AgentEnvironment EnvironmentPolicy = iota
	GitConfigEnvironment
	NPMEnvironment
	GitHubEnvironment
)

// Process is a program start requested at the operating-system seam.
type Process struct {
	Program           string
	EnvironmentPolicy EnvironmentPolicy
	Args              []string
	Identity          Identity
	Stdin             io.Reader
	Stdout, Stderr    io.Writer
}

// Effects supplies only operating-system operations; command behavior runs real.
// Reexec starts this same trusted manager executable, ignoring Program.
// ReplaceAuthorizedKey atomically stores a root-owned mode-0644 authorization file.
type Effects struct {
	Stdin io.Reader
	// HomeDirectory is evaluated only by a verified agent worker. Tests use a
	// temporary directory; production uses the sandbox's fixed home volume.
	HomeDirectory        func() (string, error)
	Identity             func() Identity
	Groups               func() ([]int, error)
	UIDs                 func() (real, effective, saved int)
	GIDs                 func() (real, effective, saved int)
	Start                func(Process) error
	Reexec               func(Process) error
	ReadFile             func(string) ([]byte, error)
	ReplaceAuthorizedKey func([]byte) error
	RemoveAuthorizedKey  func() error
}

// RunWithEffects returns the manager command's exit status at injected OS seams.
// Version answers without identity checks. Root session queries re-exec a
// trusted agent worker; only a verified agent identity may reach tmux.
func RunWithEffects(args []string, stdout, stderr io.Writer, effects Effects) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, "sandboxed-agents-manager development")
		return 0
	}
	if status, handled := sshAdministration(args, stdout, stderr, effects); handled {
		return status
	}
	if len(args) > 0 && args[0] == "agents" {
		return agents(args[1:], false, stdout, stderr, effects)
	}
	if len(args) > 1 && args[0] == "__worker" && args[1] == "agents" {
		return agents(args[2:], true, stdout, stderr, effects)
	}
	if len(args) == 1 && args[0] == "sessions" {
		if effects.Identity() == rootIdentity {
			return reexecWorker([]string{"__worker", "sessions"}, stdout, stderr, effects)
		}
		return sessions(stdout, stderr, effects)
	}
	if len(args) == 2 && args[0] == "__worker" && args[1] == "sessions" {
		return sessions(stdout, stderr, effects)
	}
	if len(args) >= 3 && args[0] == "integrations" && args[1] == "config" && args[2] == catalog.GitIntegration {
		return configureGit(args[3:], stdout, stderr, effects)
	}
	if (len(args) == 3 || len(args) == 4) && args[0] == "integrations" && args[1] == "login" {
		workflow := ""
		if len(args) == 4 {
			workflow = args[3]
		}
		if _, err := catalog.Default.ResolveLogin(args[2], workflow); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if args[2] == catalog.GitHubIntegration {
			return loginGitHub(stdout, stderr, effects)
		}
	}

	forms := append([]string{"version", "sessions", "ssh-host-key", "ssh-authorize KEY", "ssh-deauthorize"}, catalog.Default.ConfigUsage()...)
	forms = append(forms, catalog.Default.LoginUsage()...)
	fmt.Fprintln(stderr, "usage: sandboxed-agents-manager "+strings.Join(forms, " | "))
	return 2
}

func reexecWorker(args []string, stdout, stderr io.Writer, effects Effects) int {
	return exitStatus(effects.Reexec(Process{Args: args, Identity: agentIdentity, Stdout: stdout, Stderr: stderr}), stderr)
}

func sessions(stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	var tmuxStderr bytes.Buffer
	err := effects.Start(Process{Program: "tmux", Args: []string{"list-sessions", "-F", "#{session_name}"}, Identity: agentIdentity, Stdout: stdout, Stderr: &tmuxStderr})
	_, connectionMessage, _ := strings.Cut(tmuxStderr.String(), "error connecting to")
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() == 1 &&
		(strings.Contains(tmuxStderr.String(), "no server running") || strings.Contains(connectionMessage, "(No such file or directory)")) {
		return 0
	}
	fmt.Fprint(stderr, tmuxStderr.String())
	return exitStatus(err, stderr)
}

func exitStatus(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	return 1
}

func verifyAgent(effects Effects, stderr io.Writer) bool {
	if effects.UIDs == nil || effects.GIDs == nil {
		fmt.Fprintln(stderr, "agent work requires verified real, effective, and saved UID and GID 1000")
		return false
	}
	ruid, euid, suid := effects.UIDs()
	rgid, egid, sgid := effects.GIDs()
	if ruid != agentIdentity.UID || euid != agentIdentity.UID || suid != agentIdentity.UID ||
		rgid != agentIdentity.GID || egid != agentIdentity.GID || sgid != agentIdentity.GID {
		fmt.Fprintln(stderr, "agent work requires real, effective, and saved UID and GID 1000")
		return false
	}
	if effects.Groups == nil {
		fmt.Fprintln(stderr, "agent work requires verified supplementary groups")
		return false
	}
	groups, err := effects.Groups()
	if err != nil {
		fmt.Fprintf(stderr, "cannot verify agent supplementary groups: %v\n", err)
		return false
	}
	if len(groups) != 0 && !(len(groups) == 1 && groups[0] == agentIdentity.GID) {
		fmt.Fprintln(stderr, "agent work requires empty supplementary groups or only GID 1000")
		return false
	}
	return true
}
