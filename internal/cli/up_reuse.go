package cli

import "fmt"

func reusedUpConflict(request Request, reason string) error {
	return fmt.Errorf("sandbox %s: %s; use remove %s followed by up %s to change it", request.Name, reason, request.Name, request.Name)
}

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
			return reusedUpConflict(request, fmt.Sprintf("--port differs from recorded port %d", port))
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
		matching := false
		for _, mount := range container.Mounts {
			if mount.Destination == "/workspace" && mount.Type == "bind" && mount.Source == workspace {
				matching = true
			}
		}
		if !matching {
			return reusedUpConflict(request, "workspace differs from the recorded workspace")
		}
	}
	for _, option := range []string{"with", "agents"} {
		if _, supplied := request.Options[option]; supplied {
			return fmt.Errorf("up: existing sandbox --%s reuse is not implemented", option)
		}
	}
	return nil
}

func checkReusedResources(request Request, container sandboxContainer) error {
	for _, limit := range []struct {
		option    string
		recorded  *int64
		requested int64
	}{{"memory", container.HostConfig.Memory, request.Resources.Memory}, {"pids-limit", container.HostConfig.PidsLimit, request.Resources.PIDsLimit}, {"shm-size", container.HostConfig.ShmSize, request.Resources.ShmSize}} {
		if request.Options[limit.option] == "" {
			continue
		}
		if limit.recorded == nil {
			return fmt.Errorf("sandbox %s: recorded --%s is missing", request.Name, limit.option)
		}
		if *limit.recorded != limit.requested {
			return reusedUpConflict(request, fmt.Sprintf("--%s differs from recorded limit %d", limit.option, *limit.recorded))
		}
	}
	if request.Options["cpus"] != "" {
		limits := container.HostConfig
		var cpus float64
		if limits.CpuQuota != nil && limits.CpuPeriod != nil && *limits.CpuPeriod > 0 && *limits.CpuQuota > 0 {
			cpus = float64(*limits.CpuQuota) / float64(*limits.CpuPeriod)
		} else if limits.NanoCpus != nil && *limits.NanoCpus > 0 {
			cpus = float64(*limits.NanoCpus) / 1e9
		} else {
			return fmt.Errorf("sandbox %s: recorded --cpus is missing", request.Name)
		}
		if cpus != request.Resources.CPUs {
			return reusedUpConflict(request, fmt.Sprintf("--cpus differs from recorded limit %g", cpus))
		}
	}
	return nil
}
