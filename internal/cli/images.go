package cli

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
)

type resourceKind string

const (
	containerResource resourceKind = "container"
	imageResource     resourceKind = "image"
	volumeResource    resourceKind = "volume"
)

func (p podmanTarget) exists(kind resourceKind, name string) (bool, error) {
	_, err := p.query(string(kind), "exists", name)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// prepareUp creates an owned sandbox or reuses its inspected configuration under the lifecycle lock.
func prepareUp(request Request, p podmanTarget, output io.Writer) error {
	workspace := ""
	if len(request.Args) == 2 {
		workspace = request.Args[1]
	}
	if workspace != "" && p.host.Platform == "windows" {
		var err error
		workspace, err = windowsWorkspacePath(workspace)
		if err != nil {
			return err
		}
	}
	objects, err := readSandbox(request, p)
	if err != nil {
		return err
	}
	if err := objects.ownerError(); err != nil {
		return err
	}
	if objects.backup {
		return fmt.Errorf("sandbox %q has an interrupted update; use update %s", request.Name, request.Name)
	}
	if objects.exists {
		return applySandboxState(request, p, objects, output)
	}
	workspace, err = podmanWorkspacePath(request, p)
	if err != nil {
		return err
	}
	port, err := allocatePort(p, request.Options["port"])
	if err != nil {
		return err
	}
	tag, err := ensureImage(p, request.Toolchains, output)
	if err != nil {
		return err
	}
	names := DerivedNames(request.Group, request.Name)
	labels := []string{"--label", OwnerLabel + "=" + request.Group, "--label", SandboxNameLabel + "=" + request.Name}
	volumes := []string{names.Workspace, names.Home, names.SSH}
	workspaceMount := "type=volume,source=" + names.Workspace + ",destination=/workspace"
	if len(request.Args) == 2 {
		volumes = volumes[1:]
		workspaceMount = bindWorkspaceMount(workspace)
		if slices.Contains(objects.volumes, names.Workspace) {
			fmt.Fprintf(output, "unused kept workspace volume %s\n", names.Workspace)
		}
	}
	// A failed up removes only what this call created, so a retry is not refused as an existing sandbox.
	var created []string
	containerCreated := false
	fail := func(err error) error {
		if containerCreated {
			_, _ = p.query("container", "rm", "--force", names.Container)
		}
		for _, volume := range created {
			_, _ = p.query("volume", "rm", volume)
		}
		return err
	}
	for _, volume := range volumes {
		if slices.Contains(objects.volumes, volume) {
			fmt.Fprintf(output, "adopted volume %s\n", volume)
			continue
		}
		if _, err := p.query(append(append([]string{"volume", "create"}, labels...), volume)...); err != nil {
			return fail(err)
		}
		created = append(created, volume)
		fmt.Fprintf(output, "created volume %s\n", volume)
	}
	args := sandboxCreateArgs(request, port, workspaceMount, tag)
	if _, err := p.query(args...); err != nil {
		return fail(err)
	}
	containerCreated = true
	if _, err := p.query("start", names.Container); err != nil {
		return fail(err)
	}
	container, err := inspectContainer(p, names.Container)
	if err != nil {
		return fail(err)
	}
	if container.State.Running == nil {
		return fail(fmt.Errorf("read container %s: running state is missing", names.Container))
	}
	if !*container.State.Running {
		return fail(fmt.Errorf("sandbox %s is not running", names.Container))
	}
	fmt.Fprintf(output, "sandbox %s running; image %s; --publish 127.0.0.1:%d:22/tcp\n", request.Name, tag, port)
	if request.Options["ssh-config"] != "" {
		return installLifecycleSSHSetup(request, p, sandboxObjects{container: container, exists: true}, output)
	}
	return nil
}

// Podman reads --mount as CSV. Quote entire fields so a directory containing
// commas or quotes cannot add mount options or change the destination.
func bindWorkspaceMount(path string) string {
	var value strings.Builder
	writer := csv.NewWriter(&value)
	_ = writer.Write([]string{"type=bind", "source=" + path, "destination=/workspace"})
	writer.Flush()
	return strings.TrimSuffix(value.String(), "\n")
}
