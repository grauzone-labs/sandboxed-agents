package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/image"
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

func buildImage(p podmanTarget, toolchains []string, output io.Writer) (string, error) {
	return image.BuildWithToolchains(context.Background(), nil, toolchains, func(ctx context.Context, args ...string) error {
		result, err := p.query(args...)
		if result != "" {
			fmt.Fprint(output, result)
		}
		return err
	})
}

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
		if request.Options["ssh-config"] != "" {
			return changeSandboxState(request, p, output)
		}
		return reuseSandbox(request, p, objects.container, output)
	}
	workspace, err = podmanWorkspacePath(request, p)
	if err != nil {
		return err
	}
	port, err := allocatePort(p, request.Options["port"])
	if err != nil {
		return err
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		return err
	}
	tag := image.Tag(hash, request.Toolchains)
	exists, err := p.exists(imageResource, tag)
	if err != nil {
		return err
	}
	if !exists {
		tag, err = buildImage(p, request.Toolchains, output)
		if err != nil {
			return err
		}
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
	args := append([]string{"create", "--name", names.Container}, labels...)
	args = append(args, "--label", SSHPortLabel+"="+strconv.Itoa(int(port)),
		"--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw",
		"--memory", strconv.FormatInt(request.Resources.Memory, 10), "--cpus", strconv.FormatFloat(request.Resources.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.FormatInt(request.Resources.PIDsLimit, 10), "--shm-size", strconv.FormatInt(request.Resources.ShmSize, 10),
		"--publish", fmt.Sprintf("127.0.0.1:%d:22/tcp", port),
		"--mount", workspaceMount,
		"--mount", "type=volume,source="+names.Home+",destination=/home/agent",
		"--mount", "type=volume,source="+names.SSH+",destination=/etc/ssh", tag)
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

func reuseSandbox(request Request, p podmanTarget, container sandboxContainer, output io.Writer) error {
	if value := request.Options["port"]; value != "" {
		requested, _ := parseSSHPort(value) // Usage validation has already checked it.
		recorded, err := parseSSHPort(container.Config.Labels[SSHPortLabel])
		if err != nil {
			return fmt.Errorf("sandbox %s: recorded SSH port is missing or invalid", request.Name)
		}
		if requested != recorded {
			return fmt.Errorf("requested SSH port %d differs from recorded SSH port %d; use remove %s and up %s", requested, recorded, request.Name, request.Name)
		}
	}
	// Configuration reuse and SSH setup beyond the port check belong to their
	// respective stories. Refuse those options before starting an existing sandbox.
	if len(request.Args) == 2 {
		return fmt.Errorf("up: existing sandbox WORKSPACE comparison is not implemented")
	}
	for _, option := range []string{"with", "memory", "cpus", "pids-limit", "shm-size", "ssh-config", "agents"} {
		if _, supplied := request.Options[option]; supplied {
			return fmt.Errorf("up: existing sandbox --%s reuse is not implemented", option)
		}
	}
	name := DerivedNames(request.Group, request.Name).Container
	if container.State.Running == nil {
		return fmt.Errorf("read container %s: running state is missing", name)
	}
	if !*container.State.Running {
		if err := checkRecordedPort(p, container); err != nil {
			return err
		}
		if _, err := p.query("start", name); err != nil {
			return err
		}
	}
	fmt.Fprintf(output, "sandbox %s is running\n", request.Name)
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
