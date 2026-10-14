package cli

import (
	"errors"
	"fmt"
	"io"
)

func checkSandbox(request Request, p podmanTarget, output io.Writer) error {
	objects, err := readSandbox(request, p)
	if err != nil {
		return err
	}
	if !objects.exists && len(objects.volumes) == 0 && !objects.backup {
		return fmt.Errorf("sandbox %q does not exist in this controller group", request.Name)
	}
	var problems []error
	if err := objects.ownerError(); err != nil {
		problems = append(problems, err)
	}
	names := DerivedNames(request.Group, request.Name)
	if !objects.exists {
		fmt.Fprintf(output, "sandbox %s: volumes only; no container\n", request.Name)
	} else if objects.container.State.Running == nil {
		problems = append(problems, fmt.Errorf("read container %s: running state is missing", names.Container))
	} else {
		state := "stopped"
		if *objects.container.State.Running {
			state = "running"
		}
		fmt.Fprintf(output, "container %s: %s; owner %s\n", names.Container, state, objects.container.Config.Labels[OwnerLabel])
	}
	for _, volume := range objects.volumes {
		kind := "volume"
		if volume == names.Workspace {
			for _, mount := range objects.container.Mounts {
				if mount.Type == "bind" && mount.Destination == "/workspace" {
					kind = "unused workspace volume"
				}
			}
		}
		owner := objects.volumeOwners[volume]
		if owner == "" {
			owner = "missing"
		}
		fmt.Fprintf(output, "%s %s; owner %s\n", kind, volume, owner)
	}
	if objects.backup {
		problem := fmt.Errorf("backup container %s: interrupted update", names.Backup)
		if len(objects.conflicts) == 0 {
			problem = fmt.Errorf("%w; use update %s", problem, request.Name)
		}
		problems = append(problems, problem)
	}
	if objects.exists && len(objects.conflicts) == 0 && !objects.backup {
		if objects.container.State.Running != nil && *objects.container.State.Running {
			if err := checkManager(p, names.Container); err != nil {
				problems = append(problems, fmt.Errorf("container %s: manager does not answer: %w", names.Container, err))
			}
		}
		// This story supplies ownership and interrupted-update diagnostics.
		// Do not claim health before the full check NAME story verifies the rest.
		problems = append(problems, fmt.Errorf("check NAME: resource-limit and SSH diagnostics are not implemented"))
	}
	return errors.Join(problems...)
}
