package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
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

// listSandboxes inspects controller-group objects and prints their state, configuration, and reachable agent selection.
func listSandboxes(request Request, p podmanTarget, output io.Writer) error {
	names, err := sandboxInventory(request.Group, p)
	if err != nil {
		return err
	}
	hash, err := image.Hash(image.Context())
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
			state, problem := "-", err.Error()
			if ownerErr := objects.ownerError(); ownerErr != nil {
				state = "owner conflict"
				problem = ownerErr.Error() + "; " + problem
			}
			fmt.Fprintf(table, "%s\t%s\t-\t-\t-\t-\t-\t%s\n", name, state, problem)
			continue
		}
		workspace := "-"
		if slices.Contains(objects.volumes, DerivedNames(request.Group, name).Workspace) {
			workspace = "volume"
		}
		state, port, toolchains, agents, problem := "volumes only", "-", "-", "-", "-"
		if objects.exists {
			state = "stopped"
			if objects.container.State.Running == nil && len(objects.conflicts) == 0 && !objects.backup {
				state = "-"
				problem = fmt.Sprintf("read container %s: running state is missing", DerivedNames(request.Group, name).Container)
			}
			if objects.container.State.Running != nil && *objects.container.State.Running {
				state = "running"
			}
			port = objects.container.Config.Labels[SSHPortLabel]
			if port == "" {
				port = "-"
			}
			set, setErr := sandboxToolchains(objects.container)
			if setErr == nil {
				toolchains = toolchainSet(set)
			} else if objects.container.Config.Labels[ToolchainsLabel] != "" {
				problem = setErr.Error()
			}
			if objects.container.Image != "" && setErr == nil && len(objects.conflicts) == 0 && !objects.backup {
				outdated, err := sandboxImageOutdated(p, objects.container, hash)
				if err != nil {
					problem = err.Error()
				} else if outdated {
					problem = "outdated; use update " + name
				}
			}
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
		if state == "running" && checkManager(p, DerivedNames(request.Group, name).Container) == nil {
			answer, err := requestManager(request, p, "agents", "list")
			var selection []string
			if err == nil && json.Unmarshal([]byte(answer), &selection) == nil && selection != nil {
				valid := true
				for _, agent := range selection {
					if _, err := catalog.Default.ResolveAgent(agent); err != nil {
						valid = false
					}
				}
				if valid {
					agents = "none"
					if len(selection) > 0 {
						agents = strings.Join(selection, ",")
					}
				}
			}
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, state, port, workspace, toolchains, agents, strings.Join(objects.volumes, ", "), problem)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err = io.WriteString(output, rows.String())
	return err
}

// imageToolchains extracts the toolchain portion of a product image tag or returns an
// unavailable marker.
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
