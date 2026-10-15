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
	force, unpin, stop       bool
	arguments                []string
	definition               catalog.Agent
}

// parseAgents validates the manager operation, catalog entry, and operation-specific flags before home access.
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
	case "enable", "disable", "update", "status", "run", "session", "login":
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
	if r.operation == "run" {
		r.arguments = args[2:]
		return r, true
	}
	if r.operation == "login" {
		if len(args) > 3 {
			fmt.Fprintln(stderr, "agents login requires AGENT [WORKFLOW]")
			return r, false
		}
		workflow := ""
		if len(args) == 3 {
			workflow = args[2]
		}
		resolved, err := catalog.Default.ResolveAgentLogin(r.name, workflow)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
		r.arguments = r.definition.LoginWorkflows[resolved]
		return r, true
	}
	f := flag.NewFlagSet("agents "+r.operation, flag.ContinueOnError)
	f.SetOutput(stderr)
	if r.operation == "enable" {
		f.StringVar(&r.version, "version", "", "exact package version")
	}
	if r.operation == "update" {
		f.BoolVar(&r.unpin, "unpin", false, "install current version and remove pin")
	}
	if r.operation == "session" {
		f.BoolVar(&r.stop, "stop", false, "end the agent session")
	}
	if r.operation == "enable" || r.operation == "disable" || r.operation == "update" {
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

// agents sends root requests to a verified agent worker and reads or mutates the selection at its lock boundary.
func agents(args []string, worker bool, stdout, stderr io.Writer, effects Effects) int {
	r, valid := parseAgents(args, stderr)
	if !valid {
		return 2
	}
	if r.operation == "run" || r.operation == "session" || r.operation == "login" {
		return executeAgent(r, stdout, stderr, effects)
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
		if enabled {
			names, err := runningSessions(effects)
			if err != nil {
				return exitStatus(err, stderr)
			}
			status.Session = hasSession(names, r.name)
		}
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
		if !enabled && !r.force {
			fmt.Fprintf(stdout, "%s not enabled; nothing to disable\n", r.name)
			return 0
		}
		command := commandPath(home, r.definition)
		previous, _, err := mutationCommandLink(command, root, current, r)
		if err != nil {
			return exitStatus(err, stderr)
		}
		if !enabled && previous == "" {
			fmt.Fprintf(stdout, "%s not enabled; nothing to disable\n", r.name)
			return 0
		}
		if enabled {
			if err := guardAgentSession(r, stdout, effects); err != nil {
				return exitStatus(err, stderr)
			}
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
		cleanupRecoveredInstallation(root, previous, current, selection)
		if enabled {
			if err := os.RemoveAll(filepath.Join(root, current.Prefix)); err != nil {
				return exitStatus(err, stderr)
			}
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
		_, healthy, err := mutationCommandLink(commandPath(home, r.definition), root, current, r)
		if err != nil {
			return exitStatus(err, stderr)
		}
		if r.operation == "enable" && enabled && !healthy && !r.force {
			return exitStatus(fmt.Errorf("%s command is missing; retry agents enable with --force to repair it", r.name), stderr)
		}
		if r.operation == "enable" && enabled && healthy && (r.version == "" || r.version == current.Version) {
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
		if enabled {
			if err := guardAgentSession(r, stdout, effects); err != nil {
				return exitStatus(err, stderr)
			}
		}
		if r.operation == "update" && !r.unpin {
			r.version = current.Pin
		}
		if r.operation == "enable" && enabled && r.version == "" {
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

// reportInstallation prints the selection version and pin for a completed enable or update.
func reportInstallation(stdout io.Writer, name string, current installation) {
	fmt.Fprintf(stdout, "%s enabled: version %s", name, current.Version)
	if current.Pin != "" {
		fmt.Fprintf(stdout, " (pin %s)", current.Pin)
	} else {
		fmt.Fprint(stdout, " (pin none)")
	}
	fmt.Fprintln(stdout)
}

// readSelection loads the current selection and rejects malformed JSON or unsafe installation prefix names.
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

// installAgent stages a catalog npm package and verifies its version and command before publication.
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

// publishInstallation replaces a managed command link and commits its selection entry.
// A selection-write error triggers a best-effort restoration of the preceding link.
func publishInstallation(home, root string, r agentRequest, installed installation, selection map[string]installation) error {
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	command := commandPath(home, r.definition)
	old, existed := selection[r.name]
	previous, _, err := mutationCommandLink(command, root, old, r)
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
	cleanupRecoveredInstallation(root, previous, old, selection)
	if existed {
		os.RemoveAll(filepath.Join(root, old.Prefix))
	}
	return nil
}

// cleanupRecoveredInstallation best-effort removes a validated command link's
// prefix after commit when it differs from the old selection and is unreferenced.
func cleanupRecoveredInstallation(root, previous string, old installation, selection map[string]installation) {
	if previous == "" {
		return
	}
	prefix := filepath.Base(filepath.Dir(filepath.Dir(previous)))
	if prefix == old.Prefix || !strings.HasPrefix(prefix, "installation-") {
		return
	}
	for _, installed := range selection {
		if installed.Prefix == prefix {
			return
		}
	}
	os.RemoveAll(filepath.Join(root, prefix))
}

// replaceLink atomically swaps a symlink through a temporary sibling in the same directory.
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

// writeSelection syncs staged JSON before atomically replacing the selection file.
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

// commandPath locates the managed command link in the agent home.
func commandPath(home string, definition catalog.Agent) string {
	return filepath.Join(home, ".local", "bin", definition.Command)
}

// installationCommand builds the command path beneath an installation prefix.
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

// mutationCommandLink accepts the selected command or, with explicit force,
// a confined publication of the same catalog package left by an interruption.
func mutationCommandLink(command, root string, current installation, r agentRequest) (string, bool, error) {
	previous, err := os.Readlink(command)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("refusing unmanaged command %s: %w", command, err)
	}
	expected := installationCommand(root, current, r.definition)
	healthy := current.Prefix != "" && previous == expected
	if err := validateManagedTarget(root, previous, r.definition, r.force || !healthy); err != nil {
		return "", false, fmt.Errorf("refusing unmanaged command %s: %w", command, err)
	}
	if !healthy && !r.force {
		return "", false, fmt.Errorf("%s command differs from its selection; retry with --force to repair a managed installation", r.name)
	}
	return previous, healthy, nil
}

// validateManagedTarget confines a recovery link and its resolved npm command
// to one real installation directory and verifies orphan package identity.
func validateManagedTarget(root, target string, definition catalog.Agent, metadataRequired bool) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("command target is not a canonical managed path")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "installation-") || parts[1] != "bin" || parts[2] != definition.Command {
		return errors.New("command target is outside the requested managed installation")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	prefix := filepath.Join(root, parts[0])
	realPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return err
	}
	if realPrefix != filepath.Join(realRoot, parts[0]) {
		return errors.New("installation prefix resolves outside its managed directory")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	inside := func(path string) bool {
		rel, err := filepath.Rel(realPrefix, path)
		return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if !inside(resolved) {
		return errors.New("managed command resolves outside its installation")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed command is not a regular file")
	}
	if metadataRequired {
		metadataPath, err := filepath.EvalSymlinks(filepath.Join(prefix, "lib", "node_modules", definition.Install.Package, "package.json"))
		if err != nil {
			return err
		}
		if !inside(metadataPath) {
			return errors.New("package metadata resolves outside its installation")
		}
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			return err
		}
		var metadata struct{ Name, Version string }
		if err := json.Unmarshal(data, &metadata); err != nil {
			return err
		}
		if metadata.Name != definition.Install.Package {
			return errors.New("installation belongs to another package")
		}
		if err := catalog.ValidateVersion(metadata.Version); err != nil {
			return err
		}
	}
	return nil
}
