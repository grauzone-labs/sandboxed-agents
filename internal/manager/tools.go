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

// toolRequest is a validated tools operation with its catalog entry, at most one selector, the connect action of `tools connect`, and whether a status includes the connect part.
type toolRequest struct {
	operation, name, channel, version, action string
	connect                                   bool
	definition                                catalog.Tool
}

// parseTools validates the Tool operation, catalog entry, and selector flags before home access.
func parseTools(args []string, stderr io.Writer) (toolRequest, bool) {
	var r toolRequest
	if len(args) == 1 && (args[0] == "restore" || args[0] == "list") {
		r.operation = args[0]
		return r, true
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "tools requires enable, update, or status TOOL, or restore, or list")
		return r, false
	}
	r.operation, r.name = args[0], args[1]
	switch r.operation {
	case "enable", "update", "status", "connect", "delete":
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
	if r.operation == "delete" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, "tools delete requires TOOL")
			return r, false
		}
		return r, true
	}
	if r.operation == "connect" {
		if len(args) != 3 {
			fmt.Fprintln(stderr, "tools connect requires TOOL ACTION")
			return r, false
		}
		r.action = args[2]
		if _, err := r.definition.ResolveConnectAction(r.action); err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
		return r, true
	}
	f := flag.NewFlagSet("tools "+r.operation, flag.ContinueOnError)
	f.SetOutput(stderr)
	if r.operation != "status" {
		f.StringVar(&r.channel, "channel", "", "release channel")
		f.StringVar(&r.version, "version", "", "exact package version")
	} else {
		f.BoolVar(&r.connect, "connect", false, "include the account connection state, which runs upstream's status query")
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

// tools sends root requests to a verified agent worker, reads a Tool selection and its service as a snapshot, or changes the selection and starts the services of enabled Tools at the manager lock boundary.
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
	switch r.operation {
	case "connect":
		return connectTool(root, r, stdout, stderr, effects)
	case "status":
		status, err := toolStatus(home, root, r.name, r.connect, effects)
		if err != nil {
			return exitStatus(err, stderr)
		}
		return exitStatus(json.NewEncoder(stdout).Encode(status), stderr)
	case "list":
		statuses, err := enabledToolStatuses(home, root, effects)
		if err != nil {
			return exitStatus(err, stderr)
		}
		return exitStatus(json.NewEncoder(stdout).Encode(statuses), stderr)
	}
	unlock, err := lockAgentData(root)
	if err != nil {
		return exitStatus(err, stderr)
	}
	defer unlock()
	if r.operation == "restore" {
		return exitStatus(restoreToolServices(root, stdout, effects), stderr)
	}
	if r.operation == "delete" {
		// A login holds the connect lock for its whole run; a delete that finds it taken never races it.
		release, held, err := tryLock(filepath.Join(toolDirectory(root, r.name), "connect.lock"))
		if err != nil && !os.IsNotExist(err) {
			return exitStatus(err, stderr)
		}
		if err == nil {
			if !held {
				return exitStatus(fmt.Errorf("%s not deleted; a T3 Connect login is in progress; cancel it or wait, then retry", r.name), stderr)
			}
			defer release()
		}
		return exitStatus(deleteTool(home, root, r, stdout, stderr, effects), stderr)
	}
	if err := changeTool(home, toolDirectory(root, r.name), r, stdout, stderr, effects); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// toolStatus reads one Tool's selection and service state as a snapshot, without the manager lock,
// and with the account connection state when asked, which runs upstream's status query.
func toolStatus(home, root, name string, connect bool, effects Effects) (ToolStatus, error) {
	directory := toolDirectory(root, name)
	current, enabled, err := readToolSelection(directory)
	if err != nil {
		return ToolStatus{}, err
	}
	status := ToolStatus{Name: name, Enabled: enabled, InstalledVersion: current.Version, Channel: current.Channel, Pin: current.Pin}
	if enabled {
		if err := fillServiceStatus(&status, directory, effects); err != nil {
			return status, err
		}
		if connect {
			definition, err := catalog.Default.ResolveTool(name)
			if err != nil {
				return status, err
			}
			status.Connect = readConnectStatus(home, directory, name, current, definition, effects)
		}
	}
	return status, nil
}

// enabledToolStatuses reads the selection and service state of every enabled Tool of the catalog, in
// name order, without the account connection state, so that `check NAME` starts no program.
func enabledToolStatuses(home, root string, effects Effects) ([]ToolStatus, error) {
	statuses := []ToolStatus{}
	for _, name := range catalog.Default.ToolNames() {
		status, err := toolStatus(home, root, name, false, effects)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if status.Enabled {
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

// restoreToolServices starts the service of every enabled Tool whose service is not running, as the
// lifecycle commands do after the sandbox is ready, and reports every Tool it could not start.
func restoreToolServices(root string, stdout io.Writer, effects Effects) error {
	var failures []error
	for _, name := range catalog.Default.ToolNames() {
		directory := toolDirectory(root, name)
		current, enabled, err := readToolSelection(directory)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if !enabled {
			continue
		}
		definition, _ := catalog.Default.ResolveTool(name)
		if err := startService(directory, name, current.Version, definition, effects); err != nil {
			failures = append(failures, err)
			continue
		}
		fmt.Fprintf(stdout, "%s service running\n", name)
	}
	return errors.Join(failures...)
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
		// A repeated enable keeps the installation and the selection and starts
		// the service only when it is not running. A damaged installation is
		// repaired at its recorded version without a resolution.
		if healthy {
			if err := startService(directory, r.name, current.Version, r.definition, effects); err != nil {
				return err
			}
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
		if err := startService(directory, r.name, version, r.definition, effects); err != nil {
			return err
		}
		reportTool(stdout, r.name, want)
		return nil
	}
	if err := installTool(home, directory, version, current.Version, r, effects, stderr); err != nil {
		return err
	}
	// The selection and the command link change only once the service runs on
	// the new version. Until then a failure reinstates the previous version.
	if err := switchService(directory, r.name, version, current, enabled, r.definition, effects); err != nil {
		if version != current.Version {
			removeToolVersion(directory, version)
		}
		return err
	}
	if err := publishTool(home, directory, want, r.definition); err != nil {
		if version != current.Version {
			stopService(directory, r.name, effects)
			removeToolVersion(directory, version)
			if enabled {
				if restart := startService(directory, r.name, current.Version, r.definition, effects); restart != nil {
					return fmt.Errorf("%w; the previous version %s did not start again: %v", err, current.Version, restart)
				}
			}
		}
		return err
	}
	pruneToolInstallations(directory, version)
	reportTool(stdout, r.name, want)
	return nil
}

// switchService stops the service of the installed version and starts it on the new one; when the
// new version does not start, the previous version's service runs again.
func switchService(directory, name, version string, current toolSelection, enabled bool, definition catalog.Tool, effects Effects) error {
	if err := stopService(directory, name, effects); err != nil {
		return err
	}
	err := startService(directory, name, version, definition, effects)
	if err == nil {
		return nil
	}
	// Whatever the new version left behind is stopped before the previous version runs again.
	if stop := stopService(directory, name, effects); stop != nil {
		err = fmt.Errorf("%w; %v", err, stop)
	}
	if !enabled || version == current.Version {
		return err
	}
	if restart := startService(directory, name, current.Version, definition, effects); restart != nil {
		return fmt.Errorf("%w; the previous version %s did not start again either: %v", err, current.Version, restart)
	}
	return fmt.Errorf("%w; the previous version %s is running again", err, current.Version)
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

// reportTool prints the installed version, the channel or Tool pin, and the running service of a Tool.
func reportTool(stdout io.Writer, name string, current toolSelection) {
	if current.Pin != "" {
		fmt.Fprintf(stdout, "%s enabled: version %s (pin %s); service running\n", name, current.Version, current.Pin)
		return
	}
	fmt.Fprintf(stdout, "%s enabled: version %s (channel %s); service running\n", name, current.Version, current.Channel)
}
