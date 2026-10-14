package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func checkSandbox(request Request, p podmanTarget, output io.Writer) error {
	objects, err := readSandbox(request, p)
	if err != nil {
		return err
	}
	var problems []error
	if !objects.exists && len(objects.volumes) == 0 && !objects.backup {
		problems = append(problems, fmt.Errorf("sandbox %q does not exist in this controller group", request.Name))
	}
	if err := objects.ownerError(); err != nil {
		problems = append(problems, err)
	}
	names := DerivedNames(request.Group, request.Name)
	_, boundWorkspace := objects.container.workspaceBind()
	if !objects.exists {
		if len(objects.volumes) > 0 {
			fmt.Fprintf(output, "sandbox %s: volumes only; no container\n", request.Name)
		} else {
			fmt.Fprintf(output, "sandbox %s: no container\n", request.Name)
		}
	} else if objects.container.State.Running == nil {
		problems = append(problems, fmt.Errorf("read container %s: running state is missing", names.Container))
	} else {
		state := "stopped"
		if *objects.container.State.Running {
			state = "running"
		}
		owner := objects.container.Config.Labels[OwnerLabel]
		if owner == "" {
			owner = "missing"
		}
		fmt.Fprintf(output, "container %s: %s; owner %s\n", names.Container, state, owner)
	}
	if objects.exists {
		if err := reportResources(objects.container, output); err != nil {
			problems = append(problems, fmt.Errorf("container %s: %w", names.Container, err))
		}
		for _, volume := range []string{names.Workspace, names.Home, names.SSH} {
			if volume == names.Workspace && boundWorkspace {
				continue
			}
			if _, exists := objects.volumeOwners[volume]; !exists {
				problems = append(problems, fmt.Errorf("missing volume %s", volume))
			}
		}
	}
	for _, volume := range objects.volumes {
		kind := "volume"
		if volume == names.Workspace && boundWorkspace {
			kind = "unused workspace volume"
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
	if objects.exists && len(objects.conflicts) == 0 && !objects.backup && objects.container.State.Running != nil {
		if *objects.container.State.Running {
			if err := checkManager(p, names.Container); err != nil {
				problems = append(problems, fmt.Errorf("container %s: manager does not answer: %w", names.Container, err))
			} else {
				fmt.Fprintln(output, "manager: reachable")
			}
		} else {
			fmt.Fprintln(output, "manager: not checked (sandbox stopped)")
		}
	}
	if err := reportSSHSetup(request, p, objects, output); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func reportResources(container sandboxContainer, output io.Writer) error {
	var problems []error
	fmt.Fprintln(output, "resource limits (sizes in bytes):")
	for _, limit := range container.integerLimits() {
		value, err := limit.read()
		if err != nil {
			problems = append(problems, err)
			continue
		}
		fmt.Fprintf(output, "%s %d\n", limit.option, value)
	}
	cpus, err := recordedCPUs(container)
	if err != nil {
		problems = append(problems, err)
	} else {
		fmt.Fprintf(output, "cpus %s\n", strconv.FormatFloat(cpus, 'f', -1, 64))
	}
	return errors.Join(problems...)
}

func reportSSHSetup(request Request, p podmanTarget, objects sandboxObjects, output io.Writer) error {
	s, err := sshSetupPaths(request, p.host.Platform)
	if err != nil {
		return err
	}
	if _, err := os.Stat(s.entry); os.IsNotExist(err) {
		fmt.Fprintln(output, "SSH setup: not installed")
		return nil
	} else if err != nil {
		return err
	}
	fmt.Fprintln(output, "SSH setup: installed")
	if !objects.exists {
		return fmt.Errorf("sandbox %s: SSH setup is installed without a container; use ssh-config %s --remove", request.Name, request.Name)
	}
	if len(objects.conflicts) > 0 || objects.backup || objects.container.State.Running == nil {
		return nil
	}
	if !*objects.container.State.Running {
		fmt.Fprintln(output, "SSH connection: not checked (sandbox stopped)")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ssh", "-F", s.userConfig,
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "ConnectionAttempts=1",
		"-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "PermitLocalCommand=no", "-o", "ClearAllForwardings=yes", sshHostName(request), "true")
	command.WaitDelay = time.Second
	if answer, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox %s: SSH connection failed: %w: %s", request.Name, err, strings.TrimSpace(string(answer)))
	}
	fmt.Fprintln(output, "SSH connection: reachable")
	return nil
}
