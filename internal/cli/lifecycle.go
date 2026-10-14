package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	requiresContainer := request.Command != "remove" && !(request.Command == "ssh-config" && request.Options["remove"] != "")
	names := DerivedNames(request.Group, request.Name)
	exists, err := p.exists(containerResource, names.Container)
	if err != nil {
		return objects, err
	}
	objects.exists = exists
	mayKeepForeignVolumes := request.Command == "remove" && request.Options["volumes"] != "" && exists
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
			if !mayKeepForeignVolumes {
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
	if !objects.exists && len(objects.volumes) > 0 && !objects.backup && requiresContainer {
		return objects, fmt.Errorf("sandbox %q has only volumes; use up %s to adopt them", request.Name, request.Name)
	}
	if objects.backup {
		return objects, fmt.Errorf("sandbox %q has an interrupted update; use update %s", request.Name, request.Name)
	}
	if !objects.exists && requiresContainer {
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
	running := *container.State.Running
	if request.Command == "up" {
		if running && request.Options["ssh-config"] != "" {
			if err := requireSSHManager(request, p); err != nil {
				return lifecycleSSHSetupFailure(request, err)
			}
		}
		if err := checkReusedUp(request, p, container); err != nil {
			return err
		}
	}
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
	} else if request.Options["volumes"] == "" {
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
	output, err := requestManager(request, p, "sessions")
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
