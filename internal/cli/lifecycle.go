package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type sandboxContainer struct {
	Config struct{ Labels map[string]string }
	State  struct{ Running *bool }
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
		checkOwner(name, records[0].Labels)
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
	if !objects.exists && len(objects.volumes) > 0 {
		return objects, fmt.Errorf("sandbox %q has only volumes; use up %s to adopt them", request.Name, request.Name)
	}
	if objects.backup {
		return objects, fmt.Errorf("sandbox %q has an interrupted update; use update %s", request.Name, request.Name)
	}
	if !objects.exists {
		return objects, fmt.Errorf("sandbox %q has no container", request.Name)
	}
	return objects, nil
}

func changeSandboxState(request Request, p podmanTarget, output io.Writer) error {
	name := DerivedNames(request.Group, request.Name).Container
	objects, err := inspectSandbox(request, p)
	if err != nil {
		return err
	}
	if request.Command == "remove" {
		return fmt.Errorf("remove: not implemented")
	}
	container := objects.container
	if container.State.Running == nil {
		return fmt.Errorf("read container %s: running state is missing", name)
	}
	if request.Options["ssh-config"] != "" {
		return fmt.Errorf("start: SSH setup is not implemented")
	}
	running := *container.State.Running
	action, state := request.Command, "running"
	switch request.Command {
	case "start":
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
	fmt.Fprintf(output, "sandbox %s is %s\n", request.Name, state)
	return nil
}
