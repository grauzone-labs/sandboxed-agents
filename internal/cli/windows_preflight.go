package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const minimumWindowsPodmanVersion = "4.7.0"
const nativeAMD64Architecture = 9

// minimumWindows11Build is the first Windows 11 release's build number.
const minimumWindows11Build = 22000

const (
	runningMachineRequirement  = "a running Podman machine is required"
	wslMachineRequirement      = "a WSL2 Podman machine is required"
	rootlessMachineRequirement = "a rootless Podman machine is required"
)

func windowsPreflight(p podmanTarget) (podmanTarget, error) {
	var failures []error
	build, err := p.host.WindowsBuild()
	workstation, productErr := p.host.WindowsWorkstation()
	if err != nil || productErr != nil || !workstation || build < minimumWindows11Build {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (build query: %v; product query: %v)", err, productErr))
	}
	nativeArchitecture, architectureErr := p.host.WindowsNativeArchitecture()
	if architectureErr != nil {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (native architecture query: %w)", architectureErr))
	} else if nativeArchitecture != nativeAMD64Architecture {
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
			fmt.Sprintf("Podman >= %s is required", minimumWindowsPodmanVersion),
			runningMachineRequirement, wslMachineRequirement, rootlessMachineRequirement)...)
		failures = append(failures, unverifiedMachinePrerequisites(cause)...)
		return p, errors.Join(failures...)
	}
	output, err := p.query("--version")
	if err != nil || !atLeastFloor(output, minimumWindowsPodmanVersion) {
		failures = append(failures, fmt.Errorf("Podman >= %s is required (client version: %q; query: %v)", minimumWindowsPodmanVersion, strings.TrimSpace(output), err))
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
			failures = append(failures, fmt.Errorf("Podman machine >= %s is required (version query: %v)", minimumWindowsPodmanVersion, versionErr))
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

		local := p
		local.connection = ""
		controllers, controllerErr := machineControllers(local, p.connection)
		failures = append(failures, controllerFailures(controllers, controllerErr)...)
		root, automountErr := machineAutomountRoot(local, p.connection)
		if automountErr != nil {
			failures = append(failures, fmt.Errorf("read automount root for machine %q: %w", p.connection, automountErr))
		} else {
			p.automountRoot = root
		}
	}
	return p, errors.Join(failures...)
}

// machineAutomountRoot checks existence before reading so an absent file uses
// defaults while an unreadable existing file fails. Plain argv remain safe when
// the transport joins arguments; live Windows confirmation is still pending.
func machineAutomountRoot(local podmanTarget, name string) (string, error) {
	if output, err := local.query("machine", "ssh", name, "test", "-e", "/etc/wsl.conf"); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(output) == "" {
			return "/mnt/", nil
		}
		return "", err
	}
	config, err := local.query("machine", "ssh", name, "cat", "/etc/wsl.conf")
	if err != nil {
		return "", err
	}
	return parseAutomountRoot(config)
}

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

func unverifiedPrerequisites(cause error, requirements ...string) []error {
	var failures []error
	for _, requirement := range requirements {
		failures = append(failures, fmt.Errorf("%s (cannot verify: %v)", requirement, cause))
	}
	return failures
}

func unverifiedMachineSelection(cause error) error {
	failures := append([]error{cause}, unverifiedPrerequisites(cause, runningMachineRequirement, wslMachineRequirement, rootlessMachineRequirement)...)
	return errors.Join(failures...)
}

func unverifiedMachinePrerequisites(cause error) []error {
	return unverifiedPrerequisites(cause,
		fmt.Sprintf("Podman machine >= %s is required", minimumWindowsPodmanVersion),
		"rootless Podman is required", "cgroups v2 is required",
		"delegated cgroup cpu controller is required", "delegated cgroup memory controller is required", "delegated cgroup pids controller is required")
}

type machine struct {
	Name    string
	Default bool
	VMType  string
}

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
	return p, nil
}

// machineControllers uses simple read-only commands so the machine transport
// does not need to preserve a shell script's argument quoting.
func machineControllers(local podmanTarget, name string) (string, error) {
	output, err := local.query("machine", "ssh", name, "id", "-u")
	if err != nil {
		return "", err
	}
	uid := strings.TrimSpace(output)
	number, err := strconv.ParseUint(uid, 10, 32)
	if err != nil || number == 0 {
		return "", fmt.Errorf("read delegated cgroup controllers: invalid rootless machine UID %q", uid)
	}
	remote := Host{ReadFile: func(path string) ([]byte, error) {
		// Some machine transports join argv for a remote shell. Refuse a path
		// requiring quoting rather than allow shell metacharacters from cgroup names.
		if !machineReadPath.MatchString(path) {
			return nil, fmt.Errorf("unsupported machine cgroup path %q", path)
		}
		output, err := local.query("machine", "ssh", name, "cat", path)
		return []byte(output), err
	}}
	return delegatedControllers(remote, uid)
}

var machineReadPath = regexp.MustCompile(`^/[A-Za-z0-9_./:@-]+$`)
