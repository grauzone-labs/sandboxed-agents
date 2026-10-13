package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
	// The caller holds ADR-0007's lifecycle lock; existing sandbox reuse remains deferred.
	exists, err := p.exists(containerResource, DerivedNames(request.Group, request.Name).Container)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("up: existing sandbox lifecycle is not implemented; option conflicts belong to a later story")
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
	exists, err = p.exists(imageResource, tag)
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
	}
	for _, volume := range volumes {
		exists, err := p.exists(volumeResource, volume)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("up: volume %s already exists; volume adoption is not implemented", volume)
		}
		if _, err := p.query(append(append([]string{"volume", "create"}, labels...), volume)...); err != nil {
			return err
		}
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
		return err
	}
	if _, err := p.query("start", names.Container); err != nil {
		return err
	}
	container, err := inspectContainer(p, names.Container)
	if err != nil {
		return err
	}
	if container.State.Running == nil {
		return fmt.Errorf("read container %s: running state is missing", names.Container)
	}
	if !*container.State.Running {
		return fmt.Errorf("sandbox %s is not running", names.Container)
	}
	fmt.Fprintf(output, "sandbox %s running; image %s; --publish 127.0.0.1:%d:22/tcp\n", request.Name, tag, port)
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
