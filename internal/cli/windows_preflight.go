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
		failures = append(failures, fmt.Errorf("podman is required on PATH"))
		return p, errors.Join(failures...)
	}
	output, err := p.query("--version")
	if err != nil || !atLeast(output, 4, 7, 0) {
		failures = append(failures, fmt.Errorf("Podman >= 4.7.0 is required (client version: %q; query: %v)", strings.TrimSpace(output), err))
	}
	selected, err := selectWindowsTarget(p)
	if err != nil {
		failures = append(failures, err)
	} else {
		p = selected
	}
	if err == nil {
		// --version identifies the local client. The bound JSON Server.Version
		// identifies the machine's engine, so a newer client cannot mask an old engine.
		versionOutput, versionErr := p.query("version", "--format", "json")
		var versions struct{ Server *struct{ Version string } }
		if versionErr != nil || json.Unmarshal([]byte(versionOutput), &versions) != nil || versions.Server == nil || !atLeast(versions.Server.Version, 4, 7, 0) {
			failures = append(failures, fmt.Errorf("Podman machine >= 4.7.0 is required (version query: %v)", versionErr))
		}
	}
	if err == nil {
		info, infoErr := p.info()
		if infoErr != nil {
			failures = append(failures, infoErr)
		} else {
			if info.Host.CgroupVersion != "v2" {
				failures = append(failures, fmt.Errorf("cgroups v2 is required"))
			}
			if info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless {
				failures = append(failures, fmt.Errorf("rootless Podman is required"))
			}
		}
	}
	if err == nil {
		local := p
		local.connection = ""
		controllers, controllerErr := machineControllers(local, p.connection)
		failures = append(failures, controllerFailures(controllers, controllerErr)...)
	}
	return p, errors.Join(failures...)
}

type machine struct {
	Name    string
	Default bool
	VMType  string
	Running bool
}

func selectWindowsTarget(p podmanTarget) (podmanTarget, error) {
	output, err := p.query("machine", "list", "--format", "json")
	if err != nil {
		return p, err
	}
	var machines []machine
	if err := json.Unmarshal([]byte(output), &machines); err != nil {
		return p, fmt.Errorf("read Podman machine list: %w", err)
	}
	if len(machines) == 0 {
		return p, fmt.Errorf("a Podman machine is required")
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
			return p, fmt.Errorf("select one default Podman machine when several exist")
		}
	}
	if chosen.Name == "" || strings.HasPrefix(chosen.Name, "-") || strings.ContainsAny(chosen.Name, "\r\n\x00") {
		return p, fmt.Errorf("invalid Podman machine name %q", chosen.Name)
	}
	output, err = p.query("machine", "inspect", chosen.Name)
	if err != nil {
		return p, err
	}
	var inspected []struct {
		Name, State string
		Rootful     *bool
	}
	if err := json.Unmarshal([]byte(output), &inspected); err != nil || len(inspected) != 1 || inspected[0].Name != chosen.Name {
		return p, fmt.Errorf("read Podman machine inspect: invalid answer")
	}
	if inspected[0].State != "running" {
		return p, fmt.Errorf("a running Podman machine is required")
	}
	if chosen.VMType != "wsl" {
		return p, fmt.Errorf("a WSL2 Podman machine is required")
	}
	if inspected[0].Rootful == nil || *inspected[0].Rootful {
		return p, fmt.Errorf("a rootless Podman machine is required (Rootful must be reported)")
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
