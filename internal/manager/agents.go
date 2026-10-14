package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// AgentStatus aliases the shared host and manager status contract.
type AgentStatus = catalog.AgentStatus

type installation struct {
	Version string `json:"version"`
	Pin     string `json:"pin,omitempty"`
	Prefix  string `json:"prefix"`
}

type agentRequest struct {
	operation, name, version string
	force, unpin             bool
	definition               catalog.Agent
}

func parseAgents(args []string, stderr io.Writer) (agentRequest, bool) {
	var r agentRequest
	if len(args) == 1 && args[0] == "list" {
		r.operation = "list"
		return r, true
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "agents requires enable, disable, update, status AGENT, or list")
		return r, false
	}
	r.operation, r.name = args[0], args[1]
	switch r.operation {
	case "enable", "disable", "update", "status":
	default:
		fmt.Fprintln(stderr, "unknown agents command", r.operation)
		return r, false
	}
	var err error
	r.definition, err = catalog.Default.ResolveAgent(r.name)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return r, false
	}
	f := flag.NewFlagSet("agents "+r.operation, flag.ContinueOnError)
	f.SetOutput(stderr)
	if r.operation == "enable" {
		f.StringVar(&r.version, "version", "", "exact package version")
	}
	if r.operation == "update" {
		f.BoolVar(&r.unpin, "unpin", false, "install current version and remove pin")
	}
	if r.operation != "status" {
		f.BoolVar(&r.force, "force", false, "allow replacement of an active agent")
	}
	if err := f.Parse(args[2:]); err != nil {
		return r, false
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected agents arguments")
		return r, false
	}
	versionSet := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "version" {
			versionSet = true
		}
	})
	if versionSet {
		if err := catalog.ValidatePinVersion(r.version); err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
	}
	return r, true
}

func agents(args []string, worker bool, stdout, stderr io.Writer, effects Effects) int {
	r, valid := parseAgents(args, stderr)
	if !valid {
		return 2
	}
	if !worker && effects.Identity != nil && effects.Identity() == rootIdentity {
		return reexecWorker(append([]string{"__worker", "agents"}, args...), stdout, stderr, effects)
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	if effects.HomeDirectory == nil {
		return exitStatus(errors.New("agent home storage is unavailable"), stderr)
	}
	home, err := effects.HomeDirectory()
	if err != nil {
		return exitStatus(err, stderr)
	}
	root := filepath.Join(home, ".local", "share", "sandboxed-agents")
	if r.operation != "list" && r.operation != "status" {
		if err := os.MkdirAll(root, 0700); err != nil {
			return exitStatus(err, stderr)
		}
		unlock, err := lockManager(filepath.Join(root, "manager.lock"))
		if err != nil {
			return exitStatus(err, stderr)
		}
		defer unlock()
	}
	selection, err := readSelection(root)
	if err != nil {
		return exitStatus(err, stderr)
	}
	current, enabled := selection[r.name]
	switch r.operation {
	case "list":
		names := make([]string, 0, len(selection))
		for name := range selection {
			names = append(names, name)
		}
		slices.Sort(names)
		return exitStatus(json.NewEncoder(stdout).Encode(names), stderr)
	case "status":
		status := AgentStatus{Name: r.name, Enabled: enabled, InstalledVersion: current.Version, Pin: current.Pin, SignedIn: "unknown"}
		command := commandPath(home, r.definition)
		previous, linkError := managedCommandLink(command, installationCommand(root, current, r.definition))
		if enabled && r.definition.StatusProbe != nil && linkError == nil && previous != "" {
			probe := r.definition.StatusProbe
			var output bytes.Buffer
			err := effects.Start(Process{Program: command, Args: probe.Args, Identity: agentIdentity, Stdout: &output, Stderr: io.Discard})
			var fields map[string]any
			if err == nil && json.Unmarshal(output.Bytes(), &fields) == nil {
				if signedIn, valid := fields[probe.BooleanField].(bool); valid {
					status.SignedIn = "no"
					if signedIn {
						status.SignedIn = "yes"
					}
				}
			}
		}
		return exitStatus(json.NewEncoder(stdout).Encode(status), stderr)
	case "disable":
		if !enabled {
			fmt.Fprintf(stdout, "%s not enabled; nothing to disable\n", r.name)
			return 0
		}
		command := commandPath(home, r.definition)
		previous, err := managedCommandLink(command, installationCommand(root, current, r.definition))
		if err != nil {
			return exitStatus(err, stderr)
		}
		if previous != "" {
			if err := os.Remove(command); err != nil {
				return exitStatus(err, stderr)
			}
		}
		delete(selection, r.name)
		if err := writeSelection(root, selection); err != nil {
			if previous != "" {
				replaceLink(command, previous)
			}
			return exitStatus(err, stderr)
		}
		if err := os.RemoveAll(filepath.Join(root, current.Prefix)); err != nil {
			return exitStatus(err, stderr)
		}
		fmt.Fprintf(stdout, "%s disabled\n", r.name)
		if current.Pin != "" {
			fmt.Fprintf(stdout, "removed pin %s\n", current.Pin)
		}
		return 0
	case "enable", "update":
		if r.operation == "update" && !enabled {
			return exitStatus(fmt.Errorf("%s not enabled", r.name), stderr)
		}
		if r.operation == "enable" && enabled && (r.version == "" || r.version == current.Version) {
			if r.version != "" && current.Pin != r.version {
				current.Pin = r.version
				selection[r.name] = current
				if err := writeSelection(root, selection); err != nil {
					return exitStatus(err, stderr)
				}
			}
			reportInstallation(stdout, r.name, current)
			return 0
		}
		if r.operation == "update" && !r.unpin {
			r.version = current.Pin
		}
		installed, err := installAgent(root, r, effects, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := publishInstallation(home, root, r, installed, selection); err != nil {
			os.RemoveAll(filepath.Join(root, installed.Prefix))
			return exitStatus(err, stderr)
		}
		reportInstallation(stdout, r.name, installed)
		return 0
	}
	return 2
}

func reportInstallation(stdout io.Writer, name string, current installation) {
	fmt.Fprintf(stdout, "%s enabled: version %s", name, current.Version)
	if current.Pin != "" {
		fmt.Fprintf(stdout, " (pin %s)", current.Pin)
	}
	fmt.Fprintln(stdout)
}

func readSelection(root string) (map[string]installation, error) {
	selection := make(map[string]installation)
	data, err := os.ReadFile(filepath.Join(root, "selection.json"))
	if os.IsNotExist(err) {
		return selection, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &selection); err != nil {
		return nil, fmt.Errorf("cannot read agent selection: %w", err)
	}
	if selection == nil {
		return nil, errors.New("invalid agent selection")
	}
	for name, installed := range selection {
		if installed.Prefix == "" || filepath.Base(installed.Prefix) != installed.Prefix || !strings.HasPrefix(installed.Prefix, "installation-") {
			return nil, fmt.Errorf("invalid managed installation for %s", name)
		}
	}
	return selection, nil
}

func installAgent(root string, r agentRequest, effects Effects, stderr io.Writer) (installation, error) {
	if r.definition.Install.Kind != catalog.NPM {
		return installation{}, fmt.Errorf("unsupported installation kind %q", r.definition.Install.Kind)
	}
	prefix, err := os.MkdirTemp(root, "installation-")
	if err != nil {
		return installation{}, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(prefix)
		}
	}()
	packageSpec := r.definition.Install.Package
	if r.version != "" {
		packageSpec += "@" + r.version
	}
	err = effects.Start(Process{Program: "npm", Args: []string{"install", "--global", "--prefix", prefix, "--", packageSpec}, Identity: agentIdentity, EnvironmentPolicy: NPMEnvironment, Stdout: stderr, Stderr: stderr})
	if err != nil {
		return installation{}, fmt.Errorf("%s installation failed: %w", r.name, err)
	}
	data, err := os.ReadFile(filepath.Join(prefix, "lib", "node_modules", r.definition.Install.Package, "package.json"))
	if err != nil {
		return installation{}, fmt.Errorf("cannot read installed %s version: %w", r.name, err)
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return installation{}, err
	}
	if err := catalog.ValidateVersion(metadata.Version); err != nil {
		return installation{}, fmt.Errorf("invalid installed package version: %w", err)
	}
	if r.version != "" && metadata.Version != r.version {
		return installation{}, fmt.Errorf("installed %s version %s differs from requested %s", r.name, metadata.Version, r.version)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", r.definition.Command)); err != nil {
		return installation{}, fmt.Errorf("installed command %s is missing: %w", r.definition.Command, err)
	}
	keep = true
	return installation{Version: metadata.Version, Pin: r.version, Prefix: filepath.Base(prefix)}, nil
}

func publishInstallation(home, root string, r agentRequest, installed installation, selection map[string]installation) error {
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	command := commandPath(home, r.definition)
	old, existed := selection[r.name]
	expected := ""
	if existed {
		expected = installationCommand(root, old, r.definition)
	}
	previous, err := managedCommandLink(command, expected)
	if err != nil {
		return err
	}
	target := installationCommand(root, installed, r.definition)
	if err := replaceLink(command, target); err != nil {
		return err
	}
	selection[r.name] = installed
	if err := writeSelection(root, selection); err != nil {
		if previous != "" {
			replaceLink(command, previous)
		} else {
			os.Remove(command)
		}
		return err
	}
	// The selection and command now point at the new installation. Cleanup
	// must never make a committed installation look like an install failure.
	if existed {
		os.RemoveAll(filepath.Join(root, old.Prefix))
	}
	return nil
}

func replaceLink(path, target string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".agent-command-")
	if err != nil {
		return err
	}
	name := file.Name()
	file.Close()
	defer os.Remove(name)
	if err := os.Remove(name); err != nil {
		return err
	}
	if err := os.Symlink(target, name); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeSelection(root string, selection map[string]installation) error {
	data, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".selection-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(root, "selection.json"))
}

func commandPath(home string, definition catalog.Agent) string {
	return filepath.Join(home, ".local", "bin", definition.Command)
}

func installationCommand(root string, current installation, definition catalog.Agent) string {
	return filepath.Join(root, current.Prefix, "bin", definition.Command)
}

// managedCommandLink permits a missing command or the exact managed symlink,
// protecting unrelated commands during mutations and optional status probes.
func managedCommandLink(command, expected string) (string, error) {
	previous, err := os.Readlink(command)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("refusing unmanaged command %s: %w", command, err)
	}
	if expected == "" || previous != expected {
		return "", fmt.Errorf("refusing unmanaged command %s", command)
	}
	return previous, nil
}
