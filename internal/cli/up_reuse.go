package cli

import (
	"fmt"
	"strconv"
)

// reusedUpConflict names a configuration difference and the commands needed to replace it.
func reusedUpConflict(request Request, reason string) error {
	return fmt.Errorf("sandbox %s: %s; use remove %s followed by up %s to change it", request.Name, reason, request.Name, request.Name)
}

// checkReusedUp checks supplied port, resource, and workspace options before an existing sandbox starts.
func checkReusedUp(request Request, p podmanTarget, container sandboxContainer) error {
	if request.Options["port"] != "" {
		port, err := parseSSHPort(container.Config.Labels[SSHPortLabel])
		if err != nil {
			return err
		}
		requested, err := parseSSHPort(request.Options["port"])
		if err != nil {
			return err
		}
		if requested != port {
			return reusedUpConflict(request, fmt.Sprintf("--port: requested SSH port %d differs from recorded SSH port %d", requested, port))
		}
	}
	if err := checkReusedResources(request, container); err != nil {
		return err
	}
	if len(request.Args) == 2 {
		workspace, err := podmanWorkspacePath(request, p)
		if err != nil {
			return err
		}
		recorded, bound := container.workspaceBind()
		if !bound || recorded != workspace {
			previous := "volume " + DerivedNames(request.Group, request.Name).Workspace
			if bound {
				previous = "bind " + strconv.Quote(recorded)
			}
			return reusedUpConflict(request, fmt.Sprintf("workspace differs: requested bind %q; recorded workspace %s", workspace, previous))
		}
	}
	for _, option := range []string{"with", "agents"} {
		if _, supplied := request.Options[option]; supplied {
			return fmt.Errorf("up: existing sandbox --%s reuse is not implemented", option)
		}
	}
	return nil
}

// checkReusedResources compares explicitly supplied limits with Podman's recorded values.
func checkReusedResources(request Request, container sandboxContainer) error {
	requested := map[string]int64{"memory": request.Resources.Memory, "pids-limit": request.Resources.PIDsLimit, "shm-size": request.Resources.ShmSize}
	for _, limit := range container.integerLimits() {
		if request.Options[limit.option] == "" {
			continue
		}
		recorded, err := limit.read()
		if err != nil {
			return fmt.Errorf("sandbox %s: %w", request.Name, err)
		}
		if recorded != requested[limit.option] {
			return reusedUpConflict(request, fmt.Sprintf("--%s differs: requested %d; recorded limit %d", limit.option, requested[limit.option], recorded))
		}
	}
	if request.Options["cpus"] != "" {
		cpu, err := container.cpuLimit()
		if err != nil {
			return fmt.Errorf("sandbox %s: %w", request.Name, err)
		}
		if !cpu.matches(request.Resources.CPUs) {
			return reusedUpConflict(request, fmt.Sprintf("--cpus differs: requested %s; recorded limit %s", strconv.FormatFloat(request.Resources.CPUs, 'f', -1, 64), strconv.FormatFloat(cpu.displayCores(), 'f', -1, 64)))
		}
	}
	return nil
}
