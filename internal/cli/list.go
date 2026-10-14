package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// sandboxInventory uses Podman names alone to discover a group's sandboxes.
// Records are never used as authority; every listed sandbox is inspected.
func sandboxInventory(group string, p podmanTarget) ([]string, error) {
	containers, err := p.query("ps", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var records []struct{ Names []string }
	if err := json.Unmarshal([]byte(containers), &records); err != nil || records == nil {
		return nil, fmt.Errorf("read sandbox names: invalid container inventory")
	}
	volumes, err := p.query("volume", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	var volumeRecords []struct{ Name string }
	if err := json.Unmarshal([]byte(volumes), &volumeRecords); err != nil || volumeRecords == nil {
		return nil, fmt.Errorf("read sandbox names: invalid volume inventory")
	}
	names := map[string]bool{}
	add := func(name string) {
		if sandboxNamePattern.MatchString(name) {
			names[name] = true
		}
	}
	for _, record := range records {
		for _, name := range record.Names {
			for _, prefix := range []string{containerPrefix, backupPrefix} {
				if name, ok := strings.CutPrefix(name, prefix+group+"."); ok {
					add(name)
				}
			}
		}
	}
	for _, record := range volumeRecords {
		if name, ok := strings.CutPrefix(record.Name, containerPrefix+group+"."); ok {
			for _, suffix := range []string{".workspace", ".home", ".ssh"} {
				if name, ok := strings.CutSuffix(name, suffix); ok {
					add(name)
				}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}

func listSandboxes(request Request, p podmanTarget, output io.Writer) error {
	names, err := sandboxInventory(request.Group, p)
	if err != nil {
		return err
	}
	var rows strings.Builder
	table := tabwriter.NewWriter(&rows, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSTATE\tSSH PORT\tWORKSPACE\tTOOLCHAINS\tAGENTS\tVOLUMES\tPROBLEM")
	for _, name := range names {
		request.Name = name
		objects, err := readSandbox(request, p)
		if err != nil {
			return err
		}
		workspace := "-"
		if slices.Contains(objects.volumes, DerivedNames(request.Group, name).Workspace) {
			workspace = "volume"
		}
		state, port, toolchains, agents, problem := "volumes only", "-", "-", "-", "-"
		if objects.exists {
			state = "stopped"
			if objects.container.State.Running == nil && len(objects.conflicts) == 0 && !objects.backup {
				return fmt.Errorf("read container %s: running state is missing", DerivedNames(request.Group, name).Container)
			}
			if objects.container.State.Running != nil && *objects.container.State.Running {
				state = "running"
			}
			port = objects.container.Config.Labels[SSHPortLabel]
			if port == "" {
				port = "-"
			}
			toolchains = imageToolchains(objects.container.Config.Image)
			agents = "unavailable"
			for _, mount := range objects.container.Mounts {
				if mount.Destination == "/workspace" {
					workspace = mount.Type
				}
			}
		}
		if objects.backup {
			state = "update interrupted"
			problem = "use update " + name
		}
		if len(objects.conflicts) > 0 {
			state = "owner conflict"
			problem = objects.ownerError().Error()
		}
		if state == "running" && managerAvailable(p, DerivedNames(request.Group, name).Container) == nil {
			// No agents are delivered yet; the manager has no agent selection to query.
			agents = "none"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, state, port, workspace, toolchains, agents, strings.Join(objects.volumes, ", "), problem)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err = io.WriteString(output, rows.String())
	return err
}

func imageToolchains(image string) string {
	set, ok := strings.CutPrefix(image, "localhost/sandboxed-agents:")
	if !ok {
		return "-"
	}
	index := strings.LastIndex(set, "-")
	if index < 1 {
		return "-"
	}
	return set[:index]
}

func managerAvailable(p podmanTarget, container string) error {
	args, err := ManagerQueryArgs(container, "/usr/local/bin/sandboxed-agents-manager", "version")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answer, err := p.queryContext(ctx, time.Second, args...)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(answer), "sandboxed-agents-manager ") {
		return fmt.Errorf("manager did not answer its version query")
	}
	return nil
}
