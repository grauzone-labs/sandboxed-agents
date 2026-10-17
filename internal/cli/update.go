package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

const updateWasRunningLabel = "io.sandboxed-agents.update-was-running"

// updateSandbox recovers an interrupted update or replaces an outdated sandbox image while
// preserving its configuration.
func updateSandbox(request Request, p podmanTarget, output io.Writer) error {
	objects, err := requireSandbox(request, p)
	if err != nil {
		return err
	}
	if objects.backup {
		return recoverUpdate(request, p, objects, output)
	}
	plan, err := prepareSandboxUpdate(request, p, objects)
	if err != nil {
		return err
	}
	if plan == nil {
		fmt.Fprintf(output, "sandbox %s already up to date\n", request.Name)
		return nil
	}
	tag, err := ensureImage(p, plan.set, output)
	if err != nil {
		return err
	}
	return plan.replace(p, tag, output)
}

type sandboxUpdate struct {
	request Request
	objects sandboxObjects
	set     []string
}

// prepareSandboxUpdate resolves the desired image from facts read under the lifecycle lock.
func prepareSandboxUpdate(request Request, p podmanTarget, objects sandboxObjects) (*sandboxUpdate, error) {
	if objects.container.State.Running == nil {
		return nil, fmt.Errorf("sandbox %q: running state is missing", request.Name)
	}
	set := request.Toolchains
	var err error
	if request.Options["with"] == "" {
		set, err = sandboxToolchains(objects.container)
		if err != nil {
			return nil, err
		}
	}
	hash, err := image.Hash(image.Context())
	if err != nil {
		return nil, err
	}
	outdated, err := imageOutdated(p, objects.container.Image, set, hash)
	if err != nil {
		return nil, err
	}
	if !outdated {
		return nil, nil
	}
	return &sandboxUpdate{request: request, objects: objects, set: set}, nil
}

// replace applies a prepared update and restores the sandbox when replacement fails.
func (plan *sandboxUpdate) replace(p podmanTarget, tag string, output io.Writer) error {
	request, objects, set := plan.request, plan.objects, plan.set
	args, port, err := replacementArgs(request, objects, set, tag)
	if err != nil {
		return err
	}
	running := *objects.container.State.Running
	var ended sessionReport
	if running {
		ended, err = guardSessions(request, p)
		if err != nil {
			return err
		}
	}
	names := DerivedNames(request.Group, request.Name)
	if _, err = p.query("rename", names.Container, names.Backup); err != nil {
		return err
	}
	created, stopped := false, false
	rollback := func(cause error) error {
		if !created {
			exists, err := p.exists(containerResource, names.Container)
			if err != nil {
				return fmt.Errorf("%w; rollback cannot inspect replacement: %v", cause, err)
			}
			if exists {
				partial, err := inspectContainer(p, names.Container)
				if err != nil || partial.Config.Labels[OwnerLabel] != request.Group {
					return fmt.Errorf("%w; rollback cannot verify replacement owner; use Podman", cause)
				}
			}
			created = exists
		}
		if created {
			if _, err := p.query("rm", "--force", names.Container); err != nil {
				return fmt.Errorf("%w; rollback could not remove replacement: %v; backup retained", cause, err)
			}
		}
		if _, err := p.query("rename", names.Backup, names.Container); err != nil {
			return fmt.Errorf("%w; rollback could not restore name: %v; backup retained", cause, err)
		}
		if running && stopped {
			if _, err := p.query("start", names.Container); err != nil {
				return fmt.Errorf("%w; restored sandbox %s but could not start it: %v; use start %s", cause, request.Name, err, request.Name)
			}
		}
		return fmt.Errorf("%w; restored sandbox %s to its previous state", cause, request.Name)
	}
	if _, err = p.query(args...); err != nil {
		return rollback(err)
	}
	created = true
	if running {
		stopped = true
		if _, err = p.query("stop", names.Backup); err != nil {
			return rollback(err)
		}
		ended.write(output, request.Name)
	}
	if _, err = p.query("start", names.Container); err != nil {
		return rollback(err)
	}
	if err = readySandbox(p, names.Container, port); err != nil {
		return rollback(err)
	}
	if err := finishUpdate(request, p, running); err != nil {
		return err
	}
	fmt.Fprintf(output, "sandbox %s updated\n", request.Name)
	return nil
}

// finishUpdate removes the superseded backup before restoring a previously stopped state.
func finishUpdate(request Request, p podmanTarget, running bool) error {
	names := DerivedNames(request.Group, request.Name)
	if _, err := p.query("rm", names.Backup); err != nil {
		state := ""
		if !running {
			state = "; sandbox is still running"
		}
		command, shell := "podman rm "+names.Backup, ""
		if p.connection != "" {
			// The selected connection belongs to the Windows host; quote it for PowerShell.
			command = "podman --connection '" + strings.ReplaceAll(p.connection, "'", "''") + "' rm " + names.Backup
			shell = " in PowerShell"
		}
		return fmt.Errorf("sandbox %s: update succeeded%s, but backup removal failed: %w; state is update interrupted and other commands refuse it; remove the stopped backup%s: %s; update %s cleans up", request.Name, state, err, shell, command, request.Name)
	}
	if !running {
		if _, err := p.query("stop", names.Container); err != nil {
			return fmt.Errorf("sandbox %s: update succeeded but sandbox is still running: %w; use stop %s", request.Name, err, request.Name)
		}
	}
	return nil
}

// replacementArgs reconstructs only the supported configuration, refusing missing data.
func replacementArgs(request Request, objects sandboxObjects, set []string, tag string) ([]string, uint16, error) {
	c := objects.container
	names := DerivedNames(request.Group, request.Name)
	port, err := parseSSHPort(c.Config.Labels[SSHPortLabel])
	if err != nil {
		return nil, 0, fmt.Errorf("recorded SSH port is missing or invalid: %w", err)
	}
	limits := [3]int64{}
	for i, limit := range c.integerLimits() {
		value, err := limit.read()
		if err != nil {
			return nil, 0, err
		}
		if value <= 0 {
			return nil, 0, fmt.Errorf("recorded --%s must be positive", limit.option)
		}
		limits[i] = value
	}
	swap, err := c.memorySwapLimit()
	if err != nil {
		return nil, 0, err
	}
	cpu, err := c.cpuLimit()
	if err != nil {
		return nil, 0, err
	}
	request.Resources = Resources{Memory: limits[0], MemorySwap: swap, PIDsLimit: limits[1], ShmSize: limits[2], CPUs: cpu.displayCores()}
	request.Toolchains = set
	workspaceMount, err := recordedWorkspaceMount(names, objects)
	if err != nil {
		return nil, 0, err
	}
	args := sandboxCreateArgs(request, port, workspaceMount, tag, updateWasRunningLabel+"="+strconv.FormatBool(*c.State.Running))
	return args, port, nil
}

// recordedWorkspaceMount validates all sandbox storage mounts and returns the workspace creation argument.
func recordedWorkspaceMount(names ObjectNames, objects sandboxObjects) (string, error) {
	workspaceMount := "type=volume,source=" + names.Workspace + ",destination=/workspace"
	expected := map[string]string{"/workspace": names.Workspace, "/home/agent": names.Home, "/etc/ssh": names.SSH}
	seen := map[string]bool{}
	for _, mount := range objects.container.Mounts {
		volume, ok := expected[mount.Destination]
		if !ok || seen[mount.Destination] {
			return "", fmt.Errorf("unsupported recorded mount at %s", mount.Destination)
		}
		seen[mount.Destination] = true
		if mount.Destination == "/workspace" && mount.Type == "bind" && mount.Source != "" {
			workspaceMount = bindWorkspaceMount(mount.Source)
			continue
		}
		if mount.Type != "volume" || mount.Name != volume || !slices.Contains(objects.volumes, volume) {
			return "", fmt.Errorf("recorded volume at %s is missing or unsupported", mount.Destination)
		}
	}
	if len(seen) != 3 {
		return "", fmt.Errorf("recorded workspace, home, and SSH mounts are required")
	}
	return workspaceMount, nil
}
