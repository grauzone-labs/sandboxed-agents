package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type systemCalls struct {
	Identity    func() Identity
	Executable  func() (string, error)
	Environment func() []string
	Start       func(*exec.Cmd) error
}

// Run executes the manager with the sandbox's operating-system adapters.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithEffects(args, stdout, stderr, newEffects(systemCalls{
		Identity:    func() Identity { return Identity{UID: os.Geteuid(), GID: os.Getegid()} },
		Executable:  os.Executable,
		Environment: os.Environ,
		Start:       func(command *exec.Cmd) error { return command.Run() },
	}))
}

// newEffects binds manager requests to OS adapters with explicit credentials and operation-specific environment policies.
func newEffects(calls systemCalls) Effects {
	prepare := func(process Process) (*exec.Cmd, error) {
		command := exec.Command(process.Program, process.Args...)
		command.Dir = process.Directory
		command.Stdin = process.Stdin
		command.Stdout, command.Stderr = process.Stdout, process.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(process.Identity.UID), Gid: uint32(process.Identity.GID),
			// Root clears supplementary groups on the way to the worker. An
			// unprivileged worker cannot call setgroups when spawning its peers.
			NoSetGroups: calls.Identity().UID != 0,
		}, Setpgid: process.ProcessGroup}
		command.Env = agentEnvironment(calls.Environment())
		if process.EnvironmentPolicy == GitConfigEnvironment {
			command.Env = gitEnvironment(command.Env)
		}
		if process.EnvironmentPolicy == NPMEnvironment {
			command.Env = npmEnvironment(command.Env)
		}
		if process.EnvironmentPolicy == GitHubEnvironment {
			command.Env = githubEnvironment(command.Env)
		}
		if process.EnvironmentPolicy == AzureDevOpsEnvironment {
			command.Env = azureDevOpsEnvironment(command.Env)
		}
		if process.EnvironmentPolicy == AzureEnvironment {
			command.Env = azureEnvironment(command.Env)
			if process.AzureSSHHost != "" {
				path, err := calls.Executable()
				if err != nil {
					return nil, err
				}
				// Python splits BROWSER on ':' before shlex parsing, then replaces
				// every %s in each argument with the URL. Neither is escapable.
				if strings.ContainsAny(path, ":\r\n\x00") || strings.Contains(path, "%s") || path == "" {
					return nil, fmt.Errorf("manager executable path cannot be represented in Azure's browser callback")
				}
				quoted := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
				command.Env = append(command.Env, "BROWSER="+quoted+" __azure-browser "+process.AzureSSHHost+" %s")
			}
		}
		command.Env = append(command.Env, process.Environment...)
		return command, nil
	}
	run := func(process Process) error {
		command, err := prepare(process)
		if err != nil {
			return err
		}
		return calls.Start(command)
	}
	return Effects{Stdin: os.Stdin, HomeDirectory: func() (string, error) { return "/home/agent", nil }, Identity: calls.Identity, Groups: os.Getgroups, UIDs: unix.Getresuid, GIDs: unix.Getresgid, Start: run,
		ReadFile: os.ReadFile, ReplaceAuthorizedKey: func(data []byte) error { return replaceAuthorizedKey("/etc/ssh", data) }, RemoveAuthorizedKey: func() error { return removeAuthorizedKey("/etc/ssh") }, Reexec: func(process Process) error {
			path, err := calls.Executable()
			if err != nil {
				return err
			}
			process.Program = path
			return run(process)
		},
		Spawn: func(process Process) (Child, error) {
			command, err := prepare(process)
			if err != nil {
				return nil, err
			}
			if err := command.Start(); err != nil {
				return nil, err
			}
			return startedChild{command: command, group: process.ProcessGroup}, nil
		},
		Detach: func(process Process) error {
			path, err := calls.Executable()
			if err != nil {
				return err
			}
			process.Program = path
			process.Stdin = nil
			command, err := prepare(process)
			if err != nil {
				return err
			}
			// A new session detaches the supervisor from the calling terminal and
			// SSH connection; the exec ending does not end it.
			command.SysProcAttr.Setsid = true
			if err := command.Start(); err != nil {
				return err
			}
			// The supervisor is re-parented to PID 1 once this manager exits; release it now.
			return command.Process.Release()
		},
		Signal: func(pid int, sig os.Signal) error {
			signal, ok := sig.(syscall.Signal)
			if !ok {
				return fmt.Errorf("unsupported signal %v", sig)
			}
			return syscall.Kill(pid, signal)
		},
		PID: os.Getpid,
		Now: time.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		Signals: func() <-chan os.Signal {
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			return signals
		}}
}

// startedChild is a spawned service process that the supervisor waits for and signals as a group.
type startedChild struct {
	command *exec.Cmd
	group   bool
}

// PID returns the process ID of the spawned process.
func (c startedChild) PID() int { return c.command.Process.Pid }

// Wait waits for the spawned process and returns its exit error.
func (c startedChild) Wait() error { return c.command.Wait() }

// Signal sends the signal to the process; SIGKILL goes to the process group the service leads, so no descendant survives.
func (c startedChild) Signal(sig os.Signal) error {
	signal, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported signal %v", sig)
	}
	pid := c.command.Process.Pid
	if c.group && signal == syscall.SIGKILL {
		pid = -pid
	}
	return syscall.Kill(pid, signal)
}

// azureEnvironment keeps the token cache in home and removes ambient browsers.
func azureEnvironment(environment []string) []string {
	var result []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key != "AZURE_CONFIG_DIR" && key != "BROWSER" {
			result = append(result, entry)
		}
	}
	return append(result, "AZURE_CONFIG_DIR=/home/agent/.azure")
}

// githubEnvironment confines gh and Git configuration to the home volume and removes ambient authentication tokens.
func githubEnvironment(environment []string) []string {
	var result []string
	for _, entry := range gitEnvironment(environment) {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GH_CONFIG_DIR", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GH_CONFIG_DIR=/home/agent/.config/gh")
}

// agentEnvironment fixes agent's home and user variables and removes inherited tmux settings.
func agentEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "USER", "LOGNAME", "TMUX", "TMUX_TMPDIR":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "HOME=/home/agent", "USER=agent", "LOGNAME=agent")
}

// Git config always belongs to agent's home, even when the sandbox has ambient
// Git config overrides. No caller-supplied config file or injected config wins.
func gitEnvironment(environment []string) []string {
	var result []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_CONFIG") || key == "XDG_CONFIG_HOME" {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GIT_CONFIG_GLOBAL=/home/agent/.gitconfig", "XDG_CONFIG_HOME=/home/agent/.config")
}

// npm's cache and config stay in agent's home even when ambient npm overrides exist.
func npmEnvironment(environment []string) []string {
	var result []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToLower(key), "npm_config_") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "NPM_CONFIG_CACHE=/home/agent/.npm", "NPM_CONFIG_USERCONFIG=/home/agent/.npmrc")
}

// azureDevOpsEnvironment keeps credentials in the home volume and disables ambient debug logging and alternate authentication.
func azureDevOpsEnvironment(environment []string) []string {
	var result []string
	for _, entry := range gitEnvironment(environment) {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AZURE_") || strings.HasPrefix(key, "GIT_TRACE") || key == "GIT_CURL_VERBOSE" || key == "GIT_ASKPASS" || key == "SSH_ASKPASS" || key == "GIT_TERMINAL_PROMPT" {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "AZURE_CONFIG_DIR=/home/agent/.azure", "AZURE_CORE_COLLECT_TELEMETRY=0", "AZURE_CORE_LOG_LEVEL=critical", "AZURE_LOGGING_ENABLE_LOG_FILE=false", "GIT_TERMINAL_PROMPT=0")
}
