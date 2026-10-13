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

func windowsPreflight(p podmanTarget) (podmanTarget, error) {
	var failures []error
	build, err := p.host.WindowsBuild()
	workstation, productErr := p.host.WindowsWorkstation()
	if err != nil || productErr != nil || !workstation || p.host.Architecture != "amd64" || build < 22000 {
		failures = append(failures, fmt.Errorf("Windows 11 x64 is required (build query: %v; product query: %v)", err, productErr))
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
			"a running Podman machine is required", "a WSL2 Podman machine is required", "a rootless Podman machine is required")...)
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
	}
	return p, errors.Join(failures...)
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
	failures := append([]error{cause}, unverifiedPrerequisites(cause, "a running Podman machine is required", "a WSL2 Podman machine is required", "a rootless Podman machine is required")...)
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
		failures = append(failures, fmt.Errorf("a WSL2 Podman machine is required"))
	}
	output, err = p.query("machine", "inspect", chosen.Name)
	if err != nil {
		failures = append(failures, err)
		failures = append(failures, unverifiedPrerequisites(err, "a running Podman machine is required", "a rootless Podman machine is required")...)
		return p, errors.Join(failures...)
	}
	var inspected []struct {
		Name, State string
		Rootful     *bool
	}
	if err := json.Unmarshal([]byte(output), &inspected); err != nil || len(inspected) != 1 || inspected[0].Name != chosen.Name {
		cause := fmt.Errorf("read Podman machine inspect: invalid answer")
		failures = append(failures, cause)
		failures = append(failures, unverifiedPrerequisites(cause, "a running Podman machine is required", "a rootless Podman machine is required")...)
		return p, errors.Join(failures...)
	}
	if inspected[0].State != "running" {
		failures = append(failures, fmt.Errorf("a running Podman machine is required"))
	}
	if inspected[0].Rootful == nil || *inspected[0].Rootful {
		failures = append(failures, fmt.Errorf("a rootless Podman machine is required (Rootful must be reported)"))
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
