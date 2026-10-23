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
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// ToolStatus aliases the shared host and manager Tool status contract.
type ToolStatus = catalog.ToolStatus

// toolSelection is the record of an enabled Tool in its Tool directory: the installed version and
// exactly one of channel or Tool pin.
type toolSelection struct {
	Version string `json:"version"`
	Channel string `json:"channel,omitempty"`
	Pin     string `json:"pin,omitempty"`
}

// toolRequest is a validated tools operation with its catalog entry and at most one selector.
type toolRequest struct {
	operation, name, channel, version string
	definition                        catalog.Tool
}

// parseTools validates the Tool operation, catalog entry, and selector flags before home access.
func parseTools(args []string, stderr io.Writer) (toolRequest, bool) {
	var r toolRequest
	if len(args) < 2 {
		fmt.Fprintln(stderr, "tools requires enable, update, or status TOOL")
		return r, false
	}
	r.operation, r.name = args[0], args[1]
	switch r.operation {
	case "enable", "update", "status":
	default:
		fmt.Fprintln(stderr, "unknown tools command", r.operation)
		return r, false
	}
	var err error
	r.definition, err = catalog.Default.ResolveTool(r.name)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return r, false
	}
	f := flag.NewFlagSet("tools "+r.operation, flag.ContinueOnError)
	f.SetOutput(stderr)
	if r.operation != "status" {
		f.StringVar(&r.channel, "channel", "", "release channel")
		f.StringVar(&r.version, "version", "", "exact package version")
	}
	if err := f.Parse(args[2:]); err != nil {
		return r, false
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected tools arguments")
		return r, false
	}
	channelSet, versionSet := false, false
	f.Visit(func(option *flag.Flag) {
		channelSet = channelSet || option.Name == "channel"
		versionSet = versionSet || option.Name == "version"
	})
	if channelSet && versionSet {
		fmt.Fprintln(stderr, "--channel and --version conflict")
		return r, false
	}
	if channelSet {
		if err := r.definition.ValidateChannel(r.channel); err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
	}
	if versionSet {
		if err := catalog.ValidatePinVersion(r.version); err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
	}
	return r, true
}

// tools sends root requests to a verified agent worker and reads or changes a Tool selection at the manager lock boundary.
func tools(args []string, worker bool, stdout, stderr io.Writer, effects Effects) int {
	r, valid := parseTools(args, stderr)
	if !valid {
		return 2
	}
	if !worker && effects.Identity != nil && effects.Identity() == rootIdentity {
		return reexecWorker(append([]string{"__worker", "tools"}, args...), stdout, stderr, effects)
	}
	home, root, ok := verifiedAgentHome(effects, stderr)
	if !ok {
		return 1
	}
	directory := toolDirectory(root, r.name)
	if r.operation == "status" {
		current, enabled, err := readToolSelection(directory)
		if err != nil {
			return exitStatus(err, stderr)
		}
		status := ToolStatus{Name: r.name, Enabled: enabled, InstalledVersion: current.Version, Channel: current.Channel, Pin: current.Pin}
		return exitStatus(json.NewEncoder(stdout).Encode(status), stderr)
	}
	unlock, err := lockAgentData(root)
	if err != nil {
		return exitStatus(err, stderr)
	}
	defer unlock()
	if err := changeTool(home, directory, r, stdout, stderr, effects); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// changeTool resolves the requested selection, installs the exact version it names when the
// installation is missing, damaged, or different, and commits the selection only after the verified install.
func changeTool(home, directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	current, enabled, err := readToolSelection(directory)
	if err != nil {
		return err
	}
	if r.operation == "update" && !enabled {
		return fmt.Errorf("%s not enabled", r.name)
	}
	pruneToolInstallations(directory, current.Version)
	healthy := enabled && toolInstallationHealthy(home, directory, current.Version, r.definition)
	want, version := current, current.Version
	switch {
	case r.version != "":
		want = toolSelection{Pin: r.version}
	case r.channel != "":
		want = toolSelection{Channel: r.channel}
	case !enabled:
		want = toolSelection{Channel: r.definition.DefaultChannel()}
	case r.operation == "enable":
		// A repeated enable keeps the installation and the selection. A damaged
		// installation is repaired at its recorded version without a resolution.
		if healthy {
			reportTool(stdout, r.name, current)
			return nil
		}
	}
	// A healthy pinned installation is never resolved again: its version is the pin.
	if want != current || !enabled || (r.operation == "update" && want.Channel != "") {
		if version, err = resolveToolVersion(want, r, effects); err != nil {
			return err
		}
	}
	want.Version = version
	if enabled && version == current.Version && healthy {
		if want != current {
			if err := writeToolSelection(directory, want); err != nil {
				return err
			}
		}
		reportTool(stdout, r.name, want)
		return nil
	}
	if err := installTool(home, directory, version, current.Version, r, effects, stderr); err != nil {
		return err
	}
	if err := publishTool(home, directory, want, r.definition); err != nil {
		if version != current.Version {
			removeToolVersion(directory, version)
		}
		return err
	}
	pruneToolInstallations(directory, version)
	reportTool(stdout, r.name, want)
	return nil
}

// resolveToolVersion asks npm for the exact version the selection names: the newest release of a
// channel, or the pin itself, which must be published.
func resolveToolVersion(want toolSelection, r toolRequest, effects Effects) (string, error) {
	spec := want.Pin
	if want.Channel != "" {
		spec = want.Channel
	}
	var output, diagnostic bytes.Buffer
	err := effects.Start(Process{Program: "npm", Args: []string{"view", "--", r.definition.Install.Package + "@" + spec, "version"}, Identity: agentIdentity, EnvironmentPolicy: NPMEnvironment, Stdout: &output, Stderr: &diagnostic})
	lines := strings.Fields(output.String())
	if want.Pin != "" {
		if err != nil || len(lines) != 1 || lines[0] != want.Pin {
			return "", fmt.Errorf("%s version %s is not published: %s", r.name, want.Pin, processFailureSummary(diagnostic.String(), err))
		}
		return want.Pin, nil
	}
	if err != nil || len(lines) != 1 || catalog.ValidateVersion(lines[0]) != nil {
		return "", fmt.Errorf("cannot resolve %s channel %s: %s", r.name, want.Channel, processFailureSummary(diagnostic.String(), err))
	}
	return lines[0], nil
}

// processFailureSummary summarizes a process failure from the first line of its diagnostic output, else its error.
func processFailureSummary(diagnostic string, err error) string {
	for _, line := range strings.Split(diagnostic, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "no version answer"
}

// installTool installs the exact launcher version into a staging prefix, makes the launcher download
// and verify its runtime, and only then moves the prefix into place, setting a prefix of the same
// version aside until the move succeeded, so a failure leaves the installed version untouched.
func installTool(home, directory, version, installed string, r toolRequest, effects Effects, stderr io.Writer) (err error) {
	staging := filepath.Join(directory, "npm", ".next-"+version)
	defer func() {
		if err != nil {
			os.RemoveAll(staging)
			if version != installed {
				os.RemoveAll(toolRuntimeDirectory(directory, version))
			}
		}
	}()
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	// The exact version is installed. Upstream's own update follows the running
	// version's channel, so it never decides the version and is never called.
	if _, err := installNPMPackage(staging, r.name, r.definition.Install, r.definition.Command, version, effects, stderr); err != nil {
		return err
	}
	launcher := filepath.Join(staging, "bin", r.definition.Command)
	var output bytes.Buffer
	if err := effects.Start(Process{Program: launcher, Args: r.definition.VersionArgs, Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment, Environment: toolEnvironment(directory, r.definition), Stdout: &output, Stderr: stderr}); err != nil {
		return fmt.Errorf("%s runtime download for %s failed: %w", r.name, version, err)
	}
	if strings.TrimSpace(output.String()) != r.definition.VersionPrefix+version {
		return fmt.Errorf("%s runtime reported %q, want %q", r.name, strings.TrimSpace(output.String()), r.definition.VersionPrefix+version)
	}
	if !toolRuntimeComplete(directory, version) {
		return fmt.Errorf("%s runtime %s is incomplete: its completion marker is missing or names another version", r.name, version)
	}
	prefix, aside := toolPrefix(directory, version), filepath.Join(directory, "npm", ".previous-"+version)
	if err := os.RemoveAll(aside); err != nil {
		return err
	}
	replaced := false
	if _, err := os.Lstat(prefix); err == nil {
		if err := os.Rename(prefix, aside); err != nil {
			return err
		}
		replaced = true
	}
	if err := os.Rename(staging, prefix); err != nil {
		if replaced {
			os.Rename(aside, prefix)
		}
		return err
	}
	if replaced {
		os.RemoveAll(aside)
	}
	return nil
}

// publishTool points the command link at the installed launcher and commits the selection.
// A selection-write error restores the preceding link.
func publishTool(home, directory string, want toolSelection, definition catalog.Tool) error {
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	command := filepath.Join(bin, definition.Command)
	previous, err := os.Readlink(command)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("refusing unmanaged command %s: %w", command, err)
	}
	if err := replaceLink(command, toolLauncher(directory, want.Version, definition)); err != nil {
		return err
	}
	if err := writeToolSelection(directory, want); err != nil {
		if previous != "" {
			replaceLink(command, previous)
		} else {
			os.Remove(command)
		}
		return err
	}
	return nil
}

// toolInstallationHealthy reports whether the installed launcher, the command link to it, and its runtime are complete for the version.
func toolInstallationHealthy(home, directory, version string, definition catalog.Tool) bool {
	if version == "" {
		return false
	}
	launcher := toolLauncher(directory, version, definition)
	if _, err := os.Stat(launcher); err != nil {
		return false
	}
	if target, err := os.Readlink(filepath.Join(home, ".local", "bin", definition.Command)); err != nil || target != launcher {
		return false
	}
	return toolRuntimeComplete(directory, version)
}

// toolRuntimeComplete reports whether the launcher's completion marker names the version.
func toolRuntimeComplete(directory, version string) bool {
	data, err := os.ReadFile(filepath.Join(toolRuntimeDirectory(directory, version), ".install-complete"))
	return err == nil && strings.TrimSpace(string(data)) == version
}

// pruneToolInstallations removes every launcher prefix and runtime directory that the selection does not name.
func pruneToolInstallations(directory, keep string) {
	for _, parent := range []string{filepath.Join(directory, "npm"), filepath.Dir(toolRuntimeDirectory(directory, "x"))} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Name() != keep {
				os.RemoveAll(filepath.Join(parent, entry.Name()))
			}
		}
	}
}

// removeToolVersion removes the launcher prefix and runtime directory of a version that was not committed.
func removeToolVersion(directory, version string) {
	if version == "" {
		return
	}
	os.RemoveAll(toolPrefix(directory, version))
	os.RemoveAll(toolRuntimeDirectory(directory, version))
}

// toolDirectory is the one directory in home data that holds everything a Tool installs and stores.
func toolDirectory(root, name string) string {
	return filepath.Join(root, "tools", name)
}

// toolPrefix is the npm prefix of one launcher version in the Tool directory.
func toolPrefix(directory, version string) string {
	return filepath.Join(directory, "npm", version)
}

// toolLauncher is the launcher of one installed version.
func toolLauncher(directory, version string, definition catalog.Tool) string {
	return filepath.Join(toolPrefix(directory, version), "bin", definition.Command)
}

// toolOwnHome is the Tool's own home, the `home/` subdirectory of its Tool directory.
func toolOwnHome(directory string) string {
	return filepath.Join(directory, "home")
}

// toolRuntimeDirectory is the directory into which the launcher unpacks one runtime version.
func toolRuntimeDirectory(directory, version string) string {
	return filepath.Join(toolOwnHome(directory), "runtime", "versions", version)
}

// toolEnvironment moves the Tool's own state into its Tool directory.
func toolEnvironment(directory string, definition catalog.Tool) []string {
	return []string{definition.HomeVariable + "=" + toolOwnHome(directory)}
}

// readToolSelection loads the Tool selection and reports whether the Tool is enabled.
func readToolSelection(directory string) (toolSelection, bool, error) {
	var selection toolSelection
	data, err := os.ReadFile(filepath.Join(directory, "selection.json"))
	if os.IsNotExist(err) {
		return selection, false, nil
	}
	if err != nil {
		return selection, false, err
	}
	if err := json.Unmarshal(data, &selection); err != nil {
		return selection, false, fmt.Errorf("cannot read tool selection: %w", err)
	}
	if selection.Version == "" || filepath.Base(selection.Version) != selection.Version || catalog.ValidateVersion(selection.Version) != nil || (selection.Channel == "") == (selection.Pin == "") {
		return selection, false, errors.New("invalid tool selection")
	}
	return selection, true, nil
}

// writeToolSelection creates the Tool directory and atomically replaces its selection file.
func writeToolSelection(directory string, selection toolSelection) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(directory, "selection.json"), ".selection-", selection)
}

// reportTool prints the installed version and the channel or Tool pin of a Tool.
func reportTool(stdout io.Writer, name string, current toolSelection) {
	if current.Pin != "" {
		fmt.Fprintf(stdout, "%s enabled: version %s (pin %s)\n", name, current.Version, current.Pin)
		return
	}
	fmt.Fprintf(stdout, "%s enabled: version %s (channel %s)\n", name, current.Version, current.Channel)
}
