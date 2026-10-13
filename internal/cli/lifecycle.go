package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
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
	container := objects.container
	if container.State.Running == nil {
		return fmt.Errorf("read container %s: running state is missing", name)
	}
	if request.Options["ssh-config"] != "" {
		return fmt.Errorf("start: SSH setup is not implemented")
	}
	running := *container.State.Running
	if request.Command == "remove" && request.Options["volumes"] != "" {
		return fmt.Errorf("remove --volumes: not implemented (#20)")
	}
	var ended sessionReport
	if running && request.Command != "start" {
		ended, err = guardSessions(request, p)
		if err != nil {
			return err
		}
	}
	if request.Command == "remove" {
		if running {
			if _, err := p.query("stop", name); err != nil {
				return err
			}
			ended.write(output, request.Name)
		}
		if _, err := p.query("rm", name); err != nil {
			return err
		}
		fmt.Fprintf(output, "removed sandbox %s\n", request.Name)
		for _, volume := range objects.volumes {
			fmt.Fprintf(output, "kept volume %s\n", volume)
		}
		if len(objects.volumes) > 0 {
			fmt.Fprintf(output, "SSH authorization remains in the kept volumes and is replaced by the next ssh-config %s --install\n", request.Name)
		}
		// Host SSH setup installation is deferred; there are no managed SSH files
		// produced by this executable to clean up yet.
		return nil
	}
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
	ended.write(output, request.Name)
	fmt.Fprintf(output, "sandbox %s is %s\n", request.Name, state)
	return nil
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
	output, err := p.queryContext(ctx, args...)
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
