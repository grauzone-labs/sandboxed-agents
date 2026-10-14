package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type sandboxContainer struct {
	Config     struct{ Labels map[string]string }
	HostConfig struct{ Memory, PidsLimit, ShmSize, NanoCpus, CpuQuota, CpuPeriod *int64 }
	Mounts     []struct{ Type, Source, Destination string }
	State      struct{ Running *bool }
}

func inspectContainer(p podmanTarget, name string) (sandboxContainer, error) {
	var records []sandboxContainer
	output, err := p.query("container", "inspect", name)
	if err != nil {
		return sandboxContainer{}, err
	}
	if err := json.Unmarshal([]byte(output), &records); err != nil || len(records) != 1 {
		return sandboxContainer{}, fmt.Errorf("read container %s: invalid inspect answer", name)
	}
	return records[0], nil
}

type sandboxObjects struct {
	container      sandboxContainer
	exists, backup bool
	volumes        []string
	foreignVolumes []string
}

func inspectSandbox(request Request, p podmanTarget) (sandboxObjects, error) {
	var objects sandboxObjects
	names := DerivedNames(request.Group, request.Name)
	exists, err := p.exists(containerResource, names.Container)
	if err != nil {
		return objects, err
	}
	objects.exists = exists
	var conflicts []string
	checkOwner := func(name string, labels map[string]string) {
		if labels[OwnerLabel] != request.Group {
			conflicts = append(conflicts, name)
		}
	}
	if exists {
		objects.container, err = inspectContainer(p, names.Container)
		if err != nil {
			return objects, err
		}
		checkOwner(names.Container, objects.container.Config.Labels)
	}
	for _, name := range []string{names.Workspace, names.Home, names.SSH} {
		exists, err := p.exists(volumeResource, name)
		if err != nil {
			return objects, err
		}
		if !exists {
			continue
		}
		objects.volumes = append(objects.volumes, name)
		output, err := p.query("volume", "inspect", name)
		if err != nil {
			return objects, err
		}
		var records []struct{ Labels map[string]string }
		if err := json.Unmarshal([]byte(output), &records); err != nil || len(records) != 1 {
			return objects, fmt.Errorf("read volume %s: invalid inspect answer", name)
		}
		if records[0].Labels[OwnerLabel] != request.Group {
			objects.foreignVolumes = append(objects.foreignVolumes, name)
			if request.Command != "remove" || request.Options["volumes"] == "" || !objects.exists {
				checkOwner(name, records[0].Labels)
			}
		}
	}
	objects.backup, err = p.exists(containerResource, names.Backup)
	if err != nil {
		return objects, err
	}
	if objects.backup {
		backup, err := inspectContainer(p, names.Backup)
		if err != nil {
			return objects, err
		}
		checkOwner(names.Backup, backup.Config.Labels)
	}
	if !objects.exists && len(objects.volumes) == 0 && !objects.backup {
		return objects, fmt.Errorf("sandbox %q does not exist in this controller group", request.Name)
	}
	if len(conflicts) > 0 {
		return objects, fmt.Errorf("owner conflict: %s; use Podman to remove or rename the objects", strings.Join(conflicts, ", "))
	}
	if !objects.exists && len(objects.volumes) > 0 && !objects.backup && request.Command != "remove" && !(request.Command == "ssh-config" && request.Options["remove"] != "") {
		return objects, fmt.Errorf("sandbox %q has only volumes; use up %s to adopt them", request.Name, request.Name)
	}
	if objects.backup {
		return objects, fmt.Errorf("sandbox %q has an interrupted update; use update %s", request.Name, request.Name)
	}
	if !objects.exists && request.Command != "remove" && !(request.Command == "ssh-config" && request.Options["remove"] != "") {
		return objects, fmt.Errorf("sandbox %q has no container", request.Name)
	}
	return objects, nil
}

func requireRunning(request Request, objects sandboxObjects) (ObjectNames, error) {
	names := DerivedNames(request.Group, request.Name)
	if objects.container.State.Running == nil {
		return names, fmt.Errorf("read container %s: running state is missing", names.Container)
	}
	if !*objects.container.State.Running {
		return names, fmt.Errorf("sandbox %q is stopped; run sandboxed-agents start %s", request.Name, request.Name)
	}
	return names, nil
}

func changeSandboxState(request Request, p podmanTarget, output io.Writer) error {
	name := DerivedNames(request.Group, request.Name).Container
	objects, err := inspectSandbox(request, p)
	if err != nil {
		return err
	}
	container := objects.container
	if request.Command == "remove" && !objects.exists {
		return removeSandbox(request, p, objects, sessionReport{}, output)
	}
	if container.State.Running == nil {
		return fmt.Errorf("read container %s: running state is missing", name)
	}
	if request.Command == "up" && request.Options["port"] != "" {
		port, err := parseSSHPort(container.Config.Labels[SSHPortLabel])
		if err != nil {
			return err
		}
		requested, err := parseSSHPort(request.Options["port"])
		if err != nil {
			return err
		}
		if requested != port {
			return fmt.Errorf("sandbox %s: --port differs from recorded port %d; use remove %s followed by up %s to change it", request.Name, port, request.Name, request.Name)
		}
	}
	if request.Command == "up" {
		if err := checkReusedResources(request, container); err != nil {
			return err
		}
		if len(request.Args) == 2 {
			workspace := request.Args[1]
			if p.host.Platform == "windows" {
				workspace, err = windowsWorkspacePath(workspace)
				if err != nil {
					return err
				}
			}
			matching := false
			for _, mount := range container.Mounts {
				if mount.Destination == "/workspace" && mount.Type == "bind" && mount.Source == workspace {
					matching = true
				}
			}
			if !matching {
				return fmt.Errorf("sandbox %s: workspace differs from the recorded workspace; use remove %s followed by up %s to change it", request.Name, request.Name, request.Name)
			}
		}
	}
	running := *container.State.Running
	var ended sessionReport
	if running && request.Command != "start" && request.Command != "up" {
		ended, err = guardSessions(request, p)
		if err != nil {
			return err
		}
	}
	if request.Command == "remove" {
		return removeSandbox(request, p, objects, ended, output)
	}
	action, state := request.Command, "running"
	switch request.Command {
	case "start", "up":
		action = "start"
		if running {
			action = ""
		}
	case "stop":
		state = "stopped"
		if !running {
			action = ""
		}
	case "restart":
		if !running {
			action = "start"
		}
	}
	if action != "" {
		if _, err := p.query(action, name); err != nil {
			return err
		}
	}
	ended.write(output, request.Name)
	fmt.Fprintf(output, "sandbox %s is %s\n", request.Name, state)
	if request.Options["ssh-config"] != "" {
		return installLifecycleSSHSetup(request, p, objects, output)
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
			return fmt.Errorf("sandbox %s: --%s differs from recorded limit %d; use remove %s followed by up %s to change it", request.Name, limit.option, *limit.recorded, request.Name, request.Name)
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
			return fmt.Errorf("sandbox %s: --cpus differs from recorded limit %g; use remove %s followed by up %s to change it", request.Name, cpus, request.Name, request.Name)
		}
	}
	return nil
}

func removeSandbox(request Request, p podmanTarget, objects sandboxObjects, ended sessionReport, output io.Writer) error {
	name := DerivedNames(request.Group, request.Name).Container
	if objects.exists {
		if *objects.container.State.Running {
			if _, err := p.query("stop", name); err != nil {
				return err
			}
			ended.write(output, request.Name)
		}
		if _, err := p.query("rm", name); err != nil {
			return err
		}
		fmt.Fprintf(output, "removed sandbox %s\n", request.Name)
	} else {
		fmt.Fprintf(output, "sandbox %s has no container; use remove %s --volumes to delete its volumes\n", request.Name, request.Name)
	}
	var failures []error
	s, err := sshSetupPaths(request, p.host.Platform)
	if err != nil {
		failures = append(failures, err)
	} else if err := s.removeHostSetup(); err != nil {
		failures = append(failures, err)
	}
	for _, volume := range objects.volumes {
		foreign := false
		for _, name := range objects.foreignVolumes {
			foreign = foreign || name == volume
		}
		if request.Options["volumes"] == "" || foreign {
			fmt.Fprintf(output, "kept volume %s\n", volume)
			continue
		}
		if _, err := p.query("volume", "rm", volume); err != nil {
			failures = append(failures, err)
		} else {
			fmt.Fprintf(output, "removed volume %s\n", volume)
		}
	}
	if request.Options["volumes"] == "" && len(objects.volumes) > 0 {
		fmt.Fprintf(output, "SSH authorization remains in the kept volumes and is replaced by the next ssh-config %s --install\n", request.Name)
	}
	if len(objects.foreignVolumes) > 0 {
		failures = append(failures, fmt.Errorf("owner conflict: kept %s; use Podman to remove or rename the objects", strings.Join(objects.foreignVolumes, ", ")))
	}
	return errors.Join(failures...)
}

type sessionReport struct {
	names       string
	unavailable bool
}

func (r sessionReport) write(output io.Writer, name string) {
	if r.unavailable {
		fmt.Fprintf(output, "sandbox %s: sessions that may have been running were ended and cannot be named\n", name)
	} else if r.names != "" {
		fmt.Fprintf(output, "sandbox %s: ended agent sessions: %s\n", name, r.names)
	}
}

func guardSessions(request Request, p podmanTarget) (sessionReport, error) {
	args, err := ManagerQueryArgs(DerivedNames(request.Group, request.Name).Container, "/usr/local/bin/sandboxed-agents-manager", "sessions")
	if err != nil {
		return sessionReport{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Bound inherited output pipes too: killing or reaping Podman does not
	// close pipes that its descendants still hold. Other queries keep no delay.
	output, err := p.queryContext(ctx, time.Second, args...)
	if err != nil {
		if request.Options["force"] != "" {
			return sessionReport{unavailable: true}, nil
		}
		return sessionReport{}, fmt.Errorf("sandbox %q: cannot rule out running sessions; use --force (%w)", request.Name, err)
	}
	sessions := strings.TrimSpace(output)
	if sessions != "" && request.Options["force"] == "" {
		return sessionReport{}, fmt.Errorf("sandbox %q has running agent sessions: %s; use --force", request.Name, strings.ReplaceAll(sessions, "\n", ", "))
	}
	return sessionReport{names: strings.ReplaceAll(sessions, "\n", ", ")}, nil
}
