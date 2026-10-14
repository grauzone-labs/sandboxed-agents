package manager

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

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

func newEffects(calls systemCalls) Effects {
	run := func(process Process) error {
		command := exec.Command(process.Program, process.Args...)
		command.Stdout, command.Stderr = process.Stdout, process.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(process.Identity.UID), Gid: uint32(process.Identity.GID),
			// Root clears supplementary groups on the way to the worker. An
			// unprivileged worker cannot call setgroups when spawning its peers.
			NoSetGroups: calls.Identity().UID != 0,
		}}
		command.Env = agentEnvironment(calls.Environment())
		if process.EnvironmentPolicy == GitConfigEnvironment {
			command.Env = gitEnvironment(command.Env)
		}
		return calls.Start(command)
	}
	return Effects{Identity: calls.Identity, Groups: os.Getgroups, UIDs: unix.Getresuid, GIDs: unix.Getresgid, Start: run,
		ReadFile: os.ReadFile, ReplaceAuthorizedKey: func(data []byte) error { return replaceAuthorizedKey("/etc/ssh", data) }, RemoveAuthorizedKey: func() error { return removeAuthorizedKey("/etc/ssh") }, Reexec: func(process Process) error {
			path, err := calls.Executable()
			if err != nil {
				return err
			}
			process.Program = path
			return run(process)
		}}
}

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
