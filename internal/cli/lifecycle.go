package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type sandboxContainer struct {
	// Image is the immutable image ID; Config.Image is the image name supplied at creation.
	Image  string
	Config struct {
		Labels map[string]string
		Image  string
	}
	HostConfig struct{ Memory, MemorySwap, PidsLimit, ShmSize, NanoCpus, CpuQuota, CpuPeriod *int64 }
	State      struct{ Running *bool }
	Mounts     []struct {
		Type, Name, Source, Destination string
	}
}

// inspectContainer reads exactly one container record from the selected Podman target.
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
	container       sandboxContainer
	backupContainer sandboxContainer
	exists, backup  bool
	volumes         []string
	foreignVolumes  []string
	volumeOwners    map[string]string
	conflicts       []string
}

// readSandbox collects Podman facts without deciding which states a command accepts.
func readSandbox(request Request, p podmanTarget) (sandboxObjects, error) {
	var objects sandboxObjects
	objects.volumeOwners = make(map[string]string)
	names := DerivedNames(request.Group, request.Name)
	exists, err := p.exists(containerResource, names.Container)
	if err != nil {
		return objects, err
	}
	objects.exists = exists
	checkOwner := func(name string, labels map[string]string) {
		if labels[OwnerLabel] != request.Group {
			objects.conflicts = append(objects.conflicts, name)
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
		objects.volumeOwners[name] = records[0].Labels[OwnerLabel]
		if records[0].Labels[OwnerLabel] != request.Group {
			objects.foreignVolumes = append(objects.foreignVolumes, name)
			if !(request.Command == "remove" && request.Options["volumes"] != "" && objects.exists) {
				checkOwner(name, records[0].Labels)
			}
		}
	}
	objects.backup, err = p.exists(containerResource, names.Backup)
	if err != nil {
		return objects, err
	}
	if objects.backup {
		objects.backupContainer, err = inspectContainer(p, names.Backup)
		if err != nil {
			return objects, err
		}
		checkOwner(names.Backup, objects.backupContainer.Config.Labels)
	}
	return objects, nil
}

// ownerError reports the collected ownership conflicts among sandbox objects.
func (objects sandboxObjects) ownerError() error {
	if len(objects.conflicts) > 0 {
		return fmt.Errorf("owner conflict: %s; use Podman to remove or rename the objects", strings.Join(objects.conflicts, ", "))
	}
	return nil
}

// requireSandbox reads sandbox objects and enforces the command's existence, ownership, and
// interrupted-update rules.
func requireSandbox(request Request, p podmanTarget) (sandboxObjects, error) {
	requiresContainer := request.Command != "remove" && !(request.Command == "ssh-config" && request.Options["remove"] != "")
	objects, err := readSandbox(request, p)
	if err != nil {
		return objects, err
	}
	if !objects.exists && len(objects.volumes) == 0 && !objects.backup {
		return objects, fmt.Errorf("sandbox %q does not exist in this controller group", request.Name)
	}
	removeOwned := request.Command == "remove" && request.Options["volumes"] != "" && objects.exists && !objects.backup && objects.container.Config.Labels[OwnerLabel] == request.Group
	if err := objects.ownerError(); err != nil && !removeOwned {
		return objects, err
	}
	if request.Command == "update" && objects.backup {
		return objects, nil
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

// requireRunning requires a recorded running state and returns the sandbox's derived Podman
// names.
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

// changeSandboxState checks ownership and interrupted updates before applying a lifecycle command.
func changeSandboxState(request Request, p podmanTarget, output io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	return applySandboxState(request, p, objects, output)
}

// applySandboxState applies a lifecycle command to objects already checked under the lifecycle lock.
func applySandboxState(request Request, p podmanTarget, objects sandboxObjects, output io.Writer) error {
	name := DerivedNames(request.Group, request.Name).Container
	if !objects.exists {
		if request.Command == "remove" {
			return removeSandbox(request, p, objects, sessionReport{}, output)
		}
		return removeSandboxVolumes(request, p, objects, output)
	}
	container := objects.container
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
	if !running && (request.Command == "up" || request.Command == "start" || request.Command == "restart") {
		if err := checkRecordedPort(p, container); err != nil {
			return err
		}
	}
	var ended sessionReport
	if running && request.Command != "start" && request.Command != "up" {
		var err error
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

// removeSandbox removes the sandbox container and host SSH setup, deleting only requested owned
// volumes.
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
		if request.Options["volumes"] == "" || objects.volumeOwners[volume] != request.Group {
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

// removeSandboxVolumes removes requested owned volumes and reports any remaining ownership
// conflict.
func removeSandboxVolumes(request Request, p podmanTarget, objects sandboxObjects, output io.Writer) error {
	for _, volume := range objects.volumes {
		if request.Options["volumes"] != "" && objects.volumeOwners[volume] == request.Group {
			if _, err := p.query("volume", "rm", volume); err != nil {
				return err
			}
			fmt.Fprintf(output, "removed volume %s\n", volume)
			continue
		}
		fmt.Fprintf(output, "kept volume %s\n", volume)
	}
	if len(objects.volumes) > 0 && request.Options["volumes"] == "" {
		fmt.Fprintf(output, "SSH authorization remains in the kept volumes and is replaced by the next ssh-config %s --install\n", request.Name)
	}
	// Host SSH setup installation is deferred; there are no managed SSH files
	// produced by this executable to clean up yet.
	return objects.ownerError()
}

type sessionReport struct {
	names       string
	unavailable bool
}

// write prints which agent sessions ended or reports that their names were unavailable.
func (r sessionReport) write(output io.Writer, name string) {
	if r.unavailable {
		fmt.Fprintf(output, "sandbox %s: sessions that may have been running were ended and cannot be named\n", name)
	} else if r.names != "" {
		fmt.Fprintf(output, "sandbox %s: ended agent sessions: %s\n", name, r.names)
	}
}

// guardSessions refuses to end running or unverified agent sessions unless the command requests
// force.
func guardSessions(request Request, p podmanTarget) (sessionReport, error) {
	output, err := queryManagerSessions(request, p)
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
