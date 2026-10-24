package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

const minimumWindowsPodmanVersion = "4.7.0"

// IsWow64Process2 returns PE machine identifiers, identifying the native host
// even when an AMD64 process is emulated on ARM64. Only PE AMD64 is supported.
const (
	// imageFileMachineAMD64 is the accepted native PE IMAGE_FILE_MACHINE_AMD64.
	imageFileMachineAMD64 = 0x8664
	// imageFileMachineARM64 is the unsupported native PE IMAGE_FILE_MACHINE_ARM64.
	imageFileMachineARM64 = 0xAA64
)

// minimumWindows11Build is the first Windows 11 release's build number.
const minimumWindows11Build = 22000

const (
	runningMachineRequirement  = "a running Podman machine is required"
	wslMachineRequirement      = "a WSL2 Podman machine is required"
	rootlessMachineRequirement = "a rootless Podman machine is required"
)

// windowsClientVersionRequirement formats the minimum supported Windows Podman client version
// requirement.
func windowsClientVersionRequirement() string {
	return fmt.Sprintf("Podman >= %s is required", minimumWindowsPodmanVersion)
}

// windowsMachineVersionRequirement formats the minimum supported Windows Podman machine version
// requirement.
func windowsMachineVersionRequirement() string {
	return fmt.Sprintf("Podman machine >= %s is required", minimumWindowsPodmanVersion)
}

// windowsPreflight collects read-only Windows prerequisites and binds the selected running
// rootless WSL2 machine.
func windowsPreflight(p podmanTarget) (podmanTarget, error) {
	var failures []error
	clientVersionRequirement := windowsClientVersionRequirement()
	build, err := p.host.WindowsBuild()
	workstation, productErr := p.host.WindowsWorkstation()
	if err != nil || productErr != nil || !workstation || build < minimumWindows11Build {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (build query: %v; product query: %v)", err, productErr))
	}
	nativeArchitecture, architectureErr := p.host.WindowsNativeArchitecture()
	if architectureErr != nil {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (native architecture query: %w)", architectureErr))
	} else if nativeArchitecture != imageFileMachineAMD64 {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (native architecture: %d)", nativeArchitecture))
	}
	for _, name := range []string{"ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(name); err != nil {
			failures = append(failures, fmt.Errorf("%s is required on PATH", name))
		}
	}
	if _, err := exec.LookPath("podman"); err != nil {
		cause := fmt.Errorf("podman is required on PATH")
		failures = append(failures, cause)
		failures = append(failures, unverifiedPrerequisites(cause,
			clientVersionRequirement,
			runningMachineRequirement, wslMachineRequirement, rootlessMachineRequirement)...)
		failures = append(failures, unverifiedMachinePrerequisites(cause)...)
		return p, errors.Join(failures...)
	}
	output, err := p.query("--version")
	if err != nil || !atLeastFloor(output, minimumWindowsPodmanVersion) {
		failures = append(failures, fmt.Errorf("%s (client version: %q; query: %v)", clientVersionRequirement, strings.TrimSpace(output), err))
	}
	selected, err := selectWindowsTarget(p)
	if err != nil {
		failures = append(failures, err)
		failures = append(failures, unverifiedMachinePrerequisites(fmt.Errorf("no running rootless WSL2 machine was selected"))...)
	} else {
		p = selected
		// --version identifies the local client. The bound JSON Server.Version
		// identifies the machine's engine, so a newer client cannot mask an old engine.
		versionOutput, versionErr := p.query("version", "--format", "json")
		var versions struct{ Server *struct{ Version string } }
		if versionErr != nil || json.Unmarshal([]byte(versionOutput), &versions) != nil || versions.Server == nil || !atLeastFloor(versions.Server.Version, minimumWindowsPodmanVersion) {
			failures = append(failures, fmt.Errorf("%s (version query: %v)", windowsMachineVersionRequirement(), versionErr))
		}

		info, infoErr := p.info()
		if infoErr != nil {
			failures = append(failures, unverifiedPrerequisites(infoErr, "rootless Podman is required", "cgroups v2 is required")...)
		} else {
			if info.Host.CgroupVersion != "v2" {
				failures = append(failures, fmt.Errorf("cgroups v2 is required"))
			}
			if info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless {
				failures = append(failures, fmt.Errorf("rootless Podman is required"))
			}
		}

		queries, queryErr := newMachineQueries(p.connection, p.machine)
		if queryErr != nil {
			cause := fmt.Errorf("machine %q SSH configuration: %w", p.connection, queryErr)
			failures = append(failures, cause)
			for _, name := range requiredCgroupControllers {
				failures = append(failures, unverifiedPrerequisites(cause, controllerRequirement(name))...)
			}
			failures = append(failures, fmt.Errorf("read automount root for machine %q: cannot verify: %w", p.connection, cause))
		} else {
			controllers, controllerErr := machineControllers(queries)
			failures = append(failures, controllerFailures(controllers, controllerErr)...)
			root, automountErr := machineAutomountRoot(queries)
			queries.close()
			if automountErr != nil {
				failures = append(failures, fmt.Errorf("read automount root for machine %q: %w", p.connection, automountErr))
			} else {
				// Preflight accepts and records custom roots. Only a workspace bind
				// requires the default root, checked after all prerequisites pass.
				p.automountRoot = root
			}
		}
	}
	return p, errors.Join(failures...)
}

// machineAutomountRoot checks existence before reading so an absent file uses
// defaults while an unreadable existing file fails. Only the command's own exit
// status 1 with no output means absence: a client that the query bound stopped
// also ends with status 1, and that is a failed query, never the default root.
// Plain argv remain safe when the transport joins arguments for the machine's shell.
func machineAutomountRoot(queries *machineQueries) (string, error) {
	if output, err := queries.run("test", "-e", "/etc/wsl.conf"); err != nil {
		var exit *exec.ExitError
		stopped := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
		if !stopped && errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(output) == "" {
			return "/mnt/", nil
		}
		return "", err
	}
	config, err := queries.run("cat", "/etc/wsl.conf")
	if err != nil {
		return "", err
	}
	return parseAutomountRoot(config)
}

// parseAutomountRoot reads the WSL automount root setting, defaulting to /mnt/ when it is
// absent.
func parseAutomountRoot(config string) (string, error) {
	if strings.ContainsRune(config, '\x00') {
		return "", fmt.Errorf("unreadable wsl.conf configuration")
	}
	section := ""
	root := "/mnt/"
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return "", fmt.Errorf("malformed wsl.conf section")
			}
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", fmt.Errorf("malformed wsl.conf setting")
		}
		if section == "automount" && strings.TrimSpace(key) == "root" {
			root = strings.TrimSpace(value)
			if root == "" {
				return "", fmt.Errorf("wsl.conf automount root is empty")
			}
		}
	}
	return root, nil
}

// atLeastFloor keeps a named semantic-version floor in one place for comparisons and diagnostics.
func atLeastFloor(value, minimum string) bool {
	parts := versionPattern.FindStringSubmatch(minimum)
	if len(parts) != 4 {
		return false
	}
	var floor [3]int
	for index := range floor {
		component, err := strconv.Atoi(parts[index+1])
		if err != nil {
			return false
		}
		floor[index] = component
	}
	return atLeast(value, floor[0], floor[1], floor[2])
}

// unverifiedPrerequisites formats a failure for each prerequisite that the supplied cause
// prevented checking.
func unverifiedPrerequisites(cause error, requirements ...string) []error {
	var failures []error
	for _, requirement := range requirements {
		failures = append(failures, fmt.Errorf("%s (cannot verify: %v)", requirement, cause))
	}
	return failures
}

// unverifiedMachineSelection combines a selection failure with the machine requirements it left
// unverified.
func unverifiedMachineSelection(cause error) error {
	failures := append([]error{cause}, unverifiedPrerequisites(cause, runningMachineRequirement, wslMachineRequirement, rootlessMachineRequirement)...)
	return errors.Join(failures...)
}

// unverifiedMachinePrerequisites lists prerequisite failures that cannot be checked without a
// selected machine.
func unverifiedMachinePrerequisites(cause error) []error {
	requirements := []string{
		windowsMachineVersionRequirement(),
		"rootless Podman is required", "cgroups v2 is required",
	}
	for _, name := range requiredCgroupControllers {
		requirements = append(requirements, controllerRequirement(name))
	}
	return unverifiedPrerequisites(cause, requirements...)
}

type machine struct {
	Name    string
	Default bool
	VMType  string
}

// machineSSHConfig is the SSH endpoint of a Podman machine as podman machine inspect reports it
// under SSHConfig: the identity file Podman created for the machine, the loopback port its sshd
// is forwarded to, and the machine user.
type machineSSHConfig struct {
	IdentityPath   string
	Port           int
	RemoteUsername string
}

// machineUserPattern admits machine user names that cannot change the meaning of the ssh
// destination USER@localhost.
var machineUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// windowsDrivePath recognizes an absolute Windows path by its drive, so the check does not depend
// on the platform that runs it.
var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// validate refuses an SSH configuration that would change what the ssh command line means: an
// out-of-range port, a user that is not a plain user name, and an identity path that is empty,
// relative, option-shaped, or holds a line break or NUL. Spaces in the path are allowed; it travels
// as one argument.
func (c machineSSHConfig) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d is out of range", c.Port)
	}
	if !machineUserPattern.MatchString(c.RemoteUsername) {
		return fmt.Errorf("invalid machine user %q", c.RemoteUsername)
	}
	switch {
	case c.IdentityPath == "":
		return fmt.Errorf("identity file is not reported")
	case strings.ContainsAny(c.IdentityPath, "\x00\r\n"):
		return fmt.Errorf("identity file path holds a line break or NUL")
	case strings.HasPrefix(c.IdentityPath, "-"):
		return fmt.Errorf("identity file path %q starts like an option", c.IdentityPath)
	case !filepath.IsAbs(c.IdentityPath) && !windowsDrivePath.MatchString(c.IdentityPath):
		return fmt.Errorf("identity file path %q is not absolute", c.IdentityPath)
	}
	return nil
}

// selectWindowsTarget selects the sole or uniquely default machine and requires a running
// rootless WSL2 target.
func selectWindowsTarget(p podmanTarget) (podmanTarget, error) {
	output, err := p.query("machine", "list", "--format", "json")
	if err != nil {
		return p, unverifiedMachineSelection(err)
	}
	var machines []machine
	if err := json.Unmarshal([]byte(output), &machines); err != nil {
		return p, unverifiedMachineSelection(fmt.Errorf("read Podman machine list: %w", err))
	}
	if len(machines) == 0 {
		return p, unverifiedMachineSelection(fmt.Errorf("a Podman machine is required"))
	}
	chosen := machines[0]
	if len(machines) > 1 {
		defaults := 0
		for _, candidate := range machines {
			if candidate.Default {
				chosen = candidate
				defaults++
			}
		}
		if defaults != 1 {
			return p, unverifiedMachineSelection(fmt.Errorf("select one default Podman machine when several exist"))
		}
	}
	if chosen.Name == "" || strings.HasPrefix(chosen.Name, "-") || strings.ContainsAny(chosen.Name, "\r\n\x00") {
		return p, unverifiedMachineSelection(fmt.Errorf("invalid Podman machine name %q", chosen.Name))
	}
	var failures []error
	if chosen.VMType != "wsl" {
		failures = append(failures, errors.New(wslMachineRequirement))
	}
	output, err = p.query("machine", "inspect", chosen.Name)
	if err != nil {
		failures = append(failures, err)
		failures = append(failures, unverifiedPrerequisites(err, runningMachineRequirement, rootlessMachineRequirement)...)
		return p, errors.Join(failures...)
	}
	var inspected []struct {
		Name, State string
		Rootful     *bool
		SSHConfig   machineSSHConfig
	}
	if err := json.Unmarshal([]byte(output), &inspected); err != nil || len(inspected) != 1 || inspected[0].Name != chosen.Name {
		cause := fmt.Errorf("read Podman machine inspect: invalid answer")
		failures = append(failures, cause)
		failures = append(failures, unverifiedPrerequisites(cause, runningMachineRequirement, rootlessMachineRequirement)...)
		return p, errors.Join(failures...)
	}
	if inspected[0].State != "running" {
		failures = append(failures, errors.New(runningMachineRequirement))
	}
	if inspected[0].Rootful == nil || *inspected[0].Rootful {
		failures = append(failures, fmt.Errorf("%s (Rootful must be reported)", rootlessMachineRequirement))
	}
	if err := errors.Join(failures...); err != nil {
		return p, err
	}

	p.connection = chosen.Name
	p.machine = inspected[0].SSHConfig
	return p, nil
}

// machineControllers uses simple read-only commands so the machine transport
// does not need to preserve a shell script's argument quoting.
func machineControllers(queries *machineQueries) (string, error) {
	output, err := queries.run("id", "-u")
	if err != nil {
		return "", err
	}
	uid := strings.TrimSpace(output)
	number, err := strconv.ParseUint(uid, 10, 32)
	if err != nil || number == 0 {
		return "", fmt.Errorf("read delegated cgroup controllers: invalid rootless machine UID %q", uid)
	}
	readFile := func(path string) ([]byte, error) {
		// Some machine transports join argv for a remote shell. Refuse a path
		// requiring quoting rather than allow shell metacharacters from cgroup names.
		if !machineReadPath.MatchString(path) {
			return nil, fmt.Errorf("unsupported machine cgroup path %q", path)
		}
		output, err := queries.run("cat", path)
		return []byte(output), err
	}
	return delegatedControllers(readFile, uid)
}

// machineQueryTimeout bounds one read-only query inside the machine, whose answer arrives within
// a second on a running machine; a client that never answers must not stall the command.
var machineQueryTimeout = 30 * time.Second

// machineQueryWaitDelay bounds the wait for the client's output after it exits.
var machineQueryWaitDelay = time.Second

// machineQueries runs the preflight's read-only commands inside the selected machine. Each
// command is one host ssh client that the executable starts itself with the machine's inspected
// identity, port, and user, instead of podman machine ssh, whose own pipes the inbox Windows
// OpenSSH client never finishes writing to (ADR-0008). The client reads no user or system SSH
// configuration and no agent, allocates no terminal, and keeps the machine's host key in a
// private known_hosts file in the query directory: the first query of a preflight records the
// key, every later one must see the same key, and the directory is removed with close.
type machineQueries struct {
	name    string
	machine machineSSHConfig
	dir     string
}

// newMachineQueries validates the inspected SSH configuration of the named machine and creates the
// query directory; a configuration that fails validation runs no client.
func newMachineQueries(name string, machine machineSSHConfig) (*machineQueries, error) {
	if err := machine.validate(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "sandboxed-agents-machine-")
	if err != nil {
		return nil, fmt.Errorf("create the machine query directory: %w", err)
	}
	return &machineQueries{name: name, machine: machine, dir: dir}, nil
}

// close removes the query directory with its private known_hosts file.
func (q *machineQueries) close() {
	os.RemoveAll(q.dir)
}

// run executes one read-only command inside the machine and returns its standard output; the
// error of a failed query names the machine and the command and carries the client's exit status
// and diagnostics. The client's output passes through the capture package, and the query ends at
// machineQueryTimeout, which stops the client itself.
func (q *machineQueries) run(argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), machineQueryTimeout)
	defer cancel()
	args := append(machineQueryOptions(), "-i", q.machine.IdentityPath, "-p", strconv.Itoa(q.machine.Port), q.machine.RemoteUsername+"@localhost")
	command := exec.CommandContext(ctx, "ssh", append(args, argv...)...)
	command.Dir = q.dir
	command.WaitDelay = machineQueryWaitDelay
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
		return stdout.String(), fmt.Errorf("machine %s: ssh %s: %w: %s", q.name, strings.Join(argv, " "), err, diagnostics)
	}
	return stdout.String(), nil
}

// machineQueryOptions are the client options of every machine query: no configuration files, no
// prompts, no terminal, the private known_hosts file of the query directory that accepts the
// machine's Ed25519 key on first contact and refuses a changed one, no agent, only the machine's
// identity, a quiet client, and a neutral locale for the command's output.
func machineQueryOptions() []string {
	return []string{"-F", "none", "-o", "BatchMode=yes", "-o", "RequestTTY=no",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UpdateHostKeys=no",
		"-o", "UserKnownHostsFile=known_hosts", "-o", "GlobalKnownHostsFile=none", "-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes", "-o", "CheckHostIP=no",
		"-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL="}
}

var machineReadPath = regexp.MustCompile(`^/[A-Za-z0-9_./:@-]+$`)
