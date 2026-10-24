package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// Host supplies platform facts, shell input and terminal detection, and read-only
// file/user queries, plus a transient loopback probe used after preflight.
// It does not replace Podman execution or authorize persistent host mutations.
type Host struct {
	Platform, Architecture    string
	ReadFile                  func(string) ([]byte, error)
	CurrentUser               func() (*user.User, error)
	WindowsBuild              func() (uint32, error)
	WindowsWorkstation        func() (bool, error)
	WindowsNativeArchitecture func() (uint16, error)
	PortAvailable             func(uint16) error
	Stdin                     io.Reader
	IsInputTerminal           func(io.Reader) bool
	IsOutputTerminal          func(io.Writer) bool
	// WorkspacePath returns a canonical directory after protected-path checks.
	WorkspacePath func(string) (string, error)
}

// defaults fills missing host facts and query hooks with the current platform defaults.
func (h Host) defaults() Host {
	if h.Platform == "" {
		h.Platform = runtime.GOOS
	}
	if h.Architecture == "" {
		h.Architecture = runtime.GOARCH
	}
	if h.ReadFile == nil {
		h.ReadFile = os.ReadFile
	}
	if h.CurrentUser == nil {
		h.CurrentUser = user.Current
	}
	if h.WindowsBuild == nil {
		h.WindowsBuild = windowsBuild
	}
	if h.WindowsWorkstation == nil {
		h.WindowsWorkstation = windowsWorkstation
	}
	if h.WindowsNativeArchitecture == nil {
		h.WindowsNativeArchitecture = windowsNativeArchitecture
	}
	if h.PortAvailable == nil {
		h.PortAvailable = loopbackPortAvailable
	}
	if h.Stdin == nil {
		h.Stdin = os.Stdin
	}
	if h.IsInputTerminal == nil {
		h.IsInputTerminal = isInputTerminal
	}
	if h.IsOutputTerminal == nil {
		h.IsOutputTerminal = isOutputTerminal
	}
	if h.WorkspacePath == nil {
		platform := h.Platform
		h.WorkspacePath = func(path string) (string, error) {
			return workspacePathForHost(path, platform)
		}
	}
	return h
}

// selectTarget binds commands on existing sandboxes without checking build prerequisites.
func selectTarget(h Host) (podmanTarget, error) {
	p := targetForHost(h)
	if h.Platform == "windows" {
		return selectWindowsTarget(p)
	}
	return p, nil
}

// preflight checks supported host prerequisites and returns the Podman target those checks
// selected.
func preflight(h Host) (podmanTarget, error) {
	p := targetForHost(h)
	if h.Platform == "windows" {
		return windowsPreflight(p)
	}
	if h.Platform != "linux" || h.Architecture != "amd64" {
		return p, fmt.Errorf("supported hosts are Linux amd64 and Windows 11 x64")
	}
	return p, linuxPreflight(p)
}

// targetForHost creates a Podman target with the host platform's environment policy.
func targetForHost(h Host) podmanTarget {
	p := podmanTarget{host: h, environment: os.Environ}
	if h.Platform == "windows" {
		p.environment = windowsPodmanEnvironment
	}
	return p
}

const minimumLinuxPodmanMinor = 4

// linuxPreflight collects failures from read-only Linux prerequisite queries.
func linuxPreflight(p podmanTarget) error {
	h := p.host
	var failures []error
	requireProgram := func(name string) bool {
		if _, err := exec.LookPath(name); err != nil {
			failures = append(failures, fmt.Errorf("%s is required on PATH", name))
			return false
		}
		return true
	}
	if requireProgram("podman") {
		output, err := p.query("--version")
		if err != nil || !atLeast(output, 4, minimumLinuxPodmanMinor, 0) {
			failures = append(failures, fmt.Errorf("Podman >= 4.%d.0 is required (client version: %q; query: %v)", minimumLinuxPodmanMinor, strings.TrimSpace(output), err))
		}
		info, err := p.info()
		if err != nil {
			failures = append(failures, err)
			failures = append(failures, unverifiedPrerequisites(err, "rootless Podman is required", "cgroups v2 is required")...)
		} else {
			if info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless {
				failures = append(failures, fmt.Errorf("rootless Podman is required"))
			}
			if info.Host.CgroupVersion != "v2" {
				failures = append(failures, fmt.Errorf("cgroups v2 is required"))
			}
		}
	} else {
		cause := fmt.Errorf("podman is required on PATH")
		failures = append(failures, unverifiedPrerequisites(cause,
			fmt.Sprintf("Podman >= 4.%d.0 is required", minimumLinuxPodmanMinor),
			"rootless Podman is required", "cgroups v2 is required")...)
	}
	for _, name := range []string{"newuidmap", "newgidmap", "pasta", "ssh", "ssh-keygen"} {
		requireProgram(name)
	}
	account, err := h.CurrentUser()
	if err != nil || account == nil {
		failures = append(failures, fmt.Errorf("read current user for subordinate ranges: %v", err))
	} else {
		if !subordinateRange(h.ReadFile, "/etc/subuid", account) {
			failures = append(failures, fmt.Errorf("a subordinate UID range is required in /etc/subuid"))
		}
		if !subordinateRange(h.ReadFile, "/etc/subgid", account) {
			failures = append(failures, fmt.Errorf("a subordinate GID range is required in /etc/subgid"))
		}
		controllers, err := delegatedControllers(h.ReadFile, account.Uid)
		failures = append(failures, controllerFailures(controllers, err)...)
	}
	if err := lingerFailure(h); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// lingerFailure returns a preflight failure when the current user of a Linux host whose PID 1 is systemd
// has no lingering, read from the linger file systemd-logind keeps per user. Without lingering, systemd stops
// the user manager shortly after the user's last login session ends and with it every rootless sandbox (#118).
// It returns nothing when lingering is on, the host runs no systemd, or a fact cannot be read; the
// executable never enables lingering, because it changes no host configuration.
func lingerFailure(h Host) error {
	init, err := h.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(init)) != "systemd" {
		return nil
	}
	account, err := h.CurrentUser()
	if err != nil || account == nil || account.Username == "" || strings.ContainsAny(account.Username, "/\x00") {
		return nil
	}
	if _, err := h.ReadFile(path.Join("/var/lib/systemd/linger", account.Username)); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("lingering is required for %s, because rootless sandboxes stop when the last login session of %s ends; enable it with loginctl enable-linger %s", account.Username, account.Username, account.Username)
}

var requiredCgroupControllers = [...]string{"cpu", "memory", "pids"}

// controllerRequirement formats the prerequisite message for one delegated cgroup controller.
func controllerRequirement(name string) string {
	return fmt.Sprintf("delegated cgroup %s controller is required", name)
}

// controllerFailures collects query errors and failures for missing required cgroup controllers.
func controllerFailures(controllers string, queryErr error) []error {
	var failures []error
	if queryErr != nil {
		failures = append(failures, queryErr)
	}
	for _, name := range requiredCgroupControllers {
		if !slices.Contains(strings.Fields(controllers), name) {
			failures = append(failures, errors.New(controllerRequirement(name)))
		}
	}
	return failures
}

// subordinateRange reports whether the account has a valid nonempty subordinate ID range in the
// supplied file.
func subordinateRange(readFile func(string) ([]byte, error), path string, account *user.User) bool {
	data, err := readFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) != 3 || (parts[0] != account.Username && parts[0] != account.Uid) {
			continue
		}
		start, startParseErr := strconv.ParseUint(parts[1], 10, 32)
		count, countParseErr := strconv.ParseUint(parts[2], 10, 32)
		if startParseErr == nil && countParseErr == nil && count > 0 && start+count <= 1<<32 {
			return true
		}
	}
	return false
}

// delegatedControllers intersects controllers available for delegation with
// those enabled for children; availability alone does not permit resource limits.
func delegatedControllers(readFile func(string) ([]byte, error), uid string) (string, error) {
	data, err := readFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read delegated cgroup controllers: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		location, ok := strings.CutPrefix(line, "0::")
		if !ok || !strings.HasPrefix(location, "/") {
			continue
		}
		location = path.Clean(location)
		if location == "/" {
			return "", fmt.Errorf("read delegated cgroup controllers: host root is not a user delegation")
		}
		// Login sessions are siblings of the user manager under user-UID.slice.
		// That identity-specific slice also identifies the delegation to query.
		userSlice := "/user.slice/user-" + uid + ".slice"
		if location == userSlice || strings.HasPrefix(location, userSlice+"/") {
			location = userSlice + "/user@" + uid + ".service"
		}
		// systemd delegates the user manager, rather than an application's leaf
		// scope. Only use that ancestor when the process actually belongs to it.
		marker := "/user@" + uid + ".service"
		if index := strings.Index(location, marker); index >= 0 {
			end := index + len(marker)
			if end == len(location) || location[end] == '/' {
				location = location[:end]
			}
		}
		// A non-systemd host is checked at its actual unified cgroup. We never
		// walk to the host root where controllers may exist without delegation.
		controllers, err := readFile(path.Join("/sys/fs/cgroup", location, "cgroup.controllers"))
		if err != nil {
			return "", fmt.Errorf("read delegated cgroup controllers: %w", err)
		}
		subtreePath := path.Join("/sys/fs/cgroup", location, "cgroup.subtree_control")
		enabled, err := readFile(subtreePath)
		if err != nil {
			return "", fmt.Errorf("read delegated cgroup controllers: %s: %w", subtreePath, err)
		}
		var delegated []string
		enabledControllers := strings.Fields(string(enabled))
		for _, name := range strings.Fields(string(controllers)) {
			if slices.Contains(enabledControllers, name) {
				delegated = append(delegated, name)
			}
		}
		return strings.Join(delegated, " "), nil
	}
	return "", fmt.Errorf("read delegated cgroup controllers: no unified cgroup")
}

type podmanInfo struct {
	Host struct {
		Security      struct{ Rootless *bool }
		CgroupVersion string
	}
}

// info decodes rootless and cgroup facts from the selected Podman target.
func (p podmanTarget) info() (podmanInfo, error) {
	var info podmanInfo
	// JSON exposes host.security.rootless and host.cgroupVersion without parsing
	// human output. Info does not prove delegation; controller files are checked
	// separately at the current user's delegated cgroup.
	output, err := p.query("info", "--format", "json")
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return info, fmt.Errorf("read Podman info: %w", err)
	}
	return info, nil
}

type podmanTarget struct {
	host          Host
	connection    string
	automountRoot string
	environment   func() []string
	// machine is the selected Windows machine's SSH endpoint as podman machine inspect reported
	// it; the preflight's machine queries validate and use it (ADR-0008), other commands only
	// carry it.
	machine machineSSHConfig
}

// arguments prepends the selected Podman connection when one is bound.
func (p podmanTarget) arguments(args []string) []string {
	if p.connection != "" {
		return append([]string{"--connection", p.connection}, args...)
	}
	return args
}

// run streams an attached process on the selected target. Child exit statuses
// pass through unchanged; launch failures and signal termination add a diagnostic.
func (p podmanTarget) run(stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error) {
	args = p.arguments(args)
	command := exec.CommandContext(context.Background(), "podman", args...)
	command.Env = p.environment()
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	err := command.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status := exit.ExitCode(); status >= 0 {
			return status, nil
		}
		err = fmt.Errorf("terminated by a signal: %w", err)
	}
	return 1, fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
}

// query runs a Podman query and returns its standard output with failure diagnostics.
func (p podmanTarget) query(args ...string) (string, error) {
	return p.queryContext(context.Background(), 0, args...)
}

// queryContext runs a context-bound Podman query, keeping standard error out of successful
// response data.
func (p podmanTarget) queryContext(ctx context.Context, waitDelay time.Duration, args ...string) (string, error) {
	command := p.command(ctx, waitDelay, args...)
	// Only stdout is query data. Preserve both streams in failure diagnostics so
	// stderr warnings cannot corrupt successful JSON or plain-text responses. On
	// Windows the capture package gives the child handles it completes its writes
	// on (ADR-0008).
	var stdout, stderr bytes.Buffer
	err := capture.Run(command, &stdout, &stderr)
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%v)", ctx.Err(), err)
		}
		diagnostics := strings.TrimSpace(stderr.String())
		if answer := strings.TrimSpace(stdout.String()); answer != "" {
			diagnostics = strings.TrimSpace(answer + "\n" + diagnostics)
		}
		return stdout.String(), fmt.Errorf("podman %s: %w: %s", strings.Join(command.Args[1:], " "), err, diagnostics)
	}
	return stdout.String(), nil
}

// command constructs a Podman command with the selected connection, environment, and pipe wait
// limit.
func (p podmanTarget) command(ctx context.Context, waitDelay time.Duration, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "podman", p.arguments(args)...)
	command.WaitDelay = waitDelay
	command.Env = p.environment()
	return command
}

// windowsPodmanEnvironment reads current variables on each call while stripping
// ambient remote targets; target selection chooses this policy once.
func windowsPodmanEnvironment() []string {
	var environment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY":
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

var versionPattern = regexp.MustCompile(`(?:^|\s)([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+\s]|$)`)

// atLeast reports whether the version in the supplied text meets the requested semantic-version
// floor.
func atLeast(value string, major, minor, patch int) bool {
	parts := versionPattern.FindStringSubmatch(value)
	if len(parts) != 4 {
		return false
	}
	for i, want := range []int{major, minor, patch} {
		got, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return false
		}
		if got != want {
			return got > want
		}
	}
	return true
}
