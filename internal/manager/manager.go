// Package manager owns administrative SSH requests, integration workflows, agent session queries, and Tool installations inside a sandbox.
package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
	// AgentEnvironment selects the environment for agent work and session queries.
	AgentEnvironment EnvironmentPolicy = iota
	// GitConfigEnvironment selects the environment for global Git configuration.
	GitConfigEnvironment
	// NPMEnvironment selects the environment for agent package installation.
	NPMEnvironment
	// GitHubEnvironment selects the environment for GitHub authentication.
	GitHubEnvironment
	// AzureDevOpsEnvironment selects the environment for Azure DevOps authentication.
	AzureDevOpsEnvironment
	// AzureEnvironment selects the environment for Azure authentication.
	AzureEnvironment
	// SSHKeyEnvironment isolates local SSH key generation from ambient authentication and askpass settings.
	SSHKeyEnvironment
)

// Process is a program start requested at the operating-system seam.
type Process struct {
	Program           string
	Directory         string
	EnvironmentPolicy EnvironmentPolicy
	Args              []string
	Identity          Identity
	Stdin             io.Reader
	Stdout, Stderr    io.Writer
	// AzureSSHHost supplies the validated SSH alias to Azure's browser callback.
	AzureSSHHost string
	// Environment holds variables appended after the policy environment, such as a Tool's home variable.
	Environment []string
	// ProcessGroup starts the process as the leader of its own process group, so a supervisor can end every descendant.
	ProcessGroup bool
}

// Child is a started process that a supervisor waits for and signals.
type Child interface {
	// PID is the process ID of the started process.
	PID() int
	// Wait blocks until the process exits and returns its exit error.
	Wait() error
	// Signal sends the signal to the process; SIGKILL reaches its whole process group when it leads one.
	Signal(os.Signal) error
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
	// Spawn starts a process without waiting for it; the service supervisor uses it for the service.
	Spawn func(Process) (Child, error)
	// Detach starts this same trusted manager executable in a new session, with no terminal and no
	// standard input, its output on the given files, and returns once it is started; it ignores Program.
	Detach func(Process) error
	// Signal sends a signal to a process by ID; signal zero probes whether it is alive.
	Signal func(pid int, signal os.Signal) error
	// PID is the manager's own process ID.
	PID func() int
	// Now is the clock the supervisor's restart policy reads.
	Now func() time.Time
	// Sleep waits for the duration or until the context ends.
	Sleep func(context.Context, time.Duration) error
	// Signals delivers the termination signals the supervisor reacts to.
	Signals func() <-chan os.Signal
	// NotifyInterrupt delivers a Ctrl+C to the returned channel instead of ending the manager, so an
	// interactive child's interruption can be named and cleaned up; the returned function restores the default.
	NotifyInterrupt func() (<-chan os.Signal, func())
}

// RunWithEffects returns the manager command's exit status at injected OS seams.
// Version answers without identity checks. Root session queries re-exec a
// trusted agent worker; only a verified agent identity may reach tmux.
// The internal __azure-browser callback verifies agent identity and prints the
// SSH tunnel and browser URL without starting another process.
func RunWithEffects(args []string, stdout, stderr io.Writer, effects Effects) int {
	if len(args) == 3 && args[0] == "__azure-browser" {
		return azureBrowser(args[1], args[2], stdout, stderr, effects)
	}
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
	if len(args) > 0 && args[0] == "tools" {
		return tools(args[1:], false, stdout, stderr, effects)
	}
	if len(args) > 1 && args[0] == "__worker" && args[1] == "tools" {
		return tools(args[2:], true, stdout, stderr, effects)
	}
	if len(args) >= 2 && args[0] == "__supervise" {
		return supervise(args[1:], stderr, effects)
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
	if len(args) >= 3 && args[0] == "integrations" && args[1] == "config" && args[2] == catalog.SSHIntegration {
		return configureSSH(args[3:], stdout, stderr, effects)
	}
	if len(args) >= 3 && args[0] == "integrations" && args[1] == "config" && args[2] == catalog.GitIntegration {
		return configureGit(args[3:], stdout, stderr, effects)
	}
	if len(args) >= 3 && args[0] == "integrations" && args[1] == "login" && args[2] == catalog.AzureDevOpsIntegration {
		return loginAzureDevOps(args[3:], stderr, effects)
	}
	if len(args) >= 3 && args[0] == "integrations" && args[1] == "login" {
		workflow := ""
		if len(args) >= 4 {
			workflow = args[3]
		}
		resolvedWorkflow, err := catalog.Default.ResolveLogin(args[2], workflow)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if args[2] == catalog.AzureIntegration {
			var options []string
			if len(args) > 4 {
				options = args[4:]
			}
			return loginAzure(resolvedWorkflow, options, stdout, stderr, effects)
		}
		if args[2] == catalog.GitHubIntegration && len(args) <= 4 {
			return loginGitHub(stdout, stderr, effects)
		}
	}

	forms := append([]string{"version", "sessions", "ssh-host-key", "ssh-authorize KEY", "ssh-deauthorize"}, catalog.Default.ConfigUsage()...)
	forms = append(forms, catalog.Default.LoginUsage()...)
	fmt.Fprintln(stderr, "usage: sandboxed-agents-manager "+strings.Join(forms, " | "))
	return 2
}

// reexecWorker runs the trusted manager worker as agent and reports its exit status.
func reexecWorker(args []string, stdout, stderr io.Writer, effects Effects) int {
	return exitStatus(effects.Reexec(Process{Args: args, Identity: agentIdentity, Stdout: stdout, Stderr: stderr}), stderr)
}

// sessions verifies agent identity before listing running agent sessions.
func sessions(stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	return exitStatus(writeSessionNames(stdout, stderr, effects), stderr)
}

// exitStatus reports errors and preserves a positive child status, using one for other failures.
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

// verifyAgent requires all user and group IDs to be 1000 with no other supplementary groups.
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
