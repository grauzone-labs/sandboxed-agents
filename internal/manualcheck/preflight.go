package manualcheck

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PreflightOptions name the preview under test and the host facts of one run.
type PreflightOptions struct {
	// Parent is the directory that holds run directories; empty selects the user's state directory.
	Parent string
	// ID is the run ID; a fresh retest gets a new one.
	ID string
	// Commit, Version, and AssetSHA256 identify the preview under test.
	Commit, Version, AssetSHA256 string
	// Executable is the verified executable; empty resolves sandboxed-agents on PATH.
	Executable string
	// ExecutableSHA256 optionally pins the executable to the SHA256SUMS entry of the preview.
	ExecutableSHA256 string
	// Group is the dedicated controller group.
	Group string
	// Platform is the record platform; empty selects the host's.
	Platform string
	// PodmanConnection names the Podman machine on Windows; empty selects the default machine.
	PodmanConnection string
	// MinFreeBytes is the space gate: on Linux the filesystem that holds Podman's storage, on Windows
	// the drive that holds the user profile, where the machine's disk lives.
	MinFreeBytes uint64
}

// Preflight verifies the executable against the preview under test, writes run.json, proves the
// controller group, records safe host diagnostics, takes the whole-engine inventory, and checks
// the free space. Nothing here changes the host. It returns the verified run.
func Preflight(options PreflightOptions) (*Run, error) {
	if options.ID == "" || options.Commit == "" || options.Version == "" || options.AssetSHA256 == "" {
		return nil, errors.New("run ID, commit, version, and asset-sha256 are required")
	}
	if !commitPattern.MatchString(options.Commit) {
		return nil, errors.New("commit must be 40 lowercase hexadecimal characters")
	}
	if !sha256Pattern.MatchString(options.AssetSHA256) {
		return nil, errors.New("asset-sha256 must be 64 lowercase hexadecimal characters")
	}
	if options.Group == "" {
		options.Group = "manual-check"
	}
	if !groupPattern.MatchString(options.Group) || options.Group == "default" {
		return nil, errors.New("group must match ^[a-z0-9][a-z0-9-]*$ and not be default")
	}
	if options.Platform == "" {
		options.Platform = DefaultPlatform()
	}
	if options.Platform != PlatformLinux && options.Platform != PlatformWindows {
		return nil, errors.New("platform must be linux or windows-11")
	}
	executable := options.Executable
	if executable == "" {
		found, err := exec.LookPath("sandboxed-agents")
		if err != nil {
			return nil, errors.New("sandboxed-agents is not on PATH; pass the executable")
		}
		executable = found
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	root, err := RunRoot(options.Parent, options.ID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "run.json")); err == nil {
		return nil, fmt.Errorf("run %s already has run.json; a fresh retest uses a new run ID", options.ID)
	}
	dir := filepath.Join(root, "preflight")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	// 1. The executable is the one of the preview under test.
	fileSHA, err := VerifyExecutable(executable, options.Version, options.AssetSHA256)
	if err != nil {
		return nil, err
	}
	if options.ExecutableSHA256 != "" && fileSHA != options.ExecutableSHA256 {
		return nil, fmt.Errorf("executable SHA-256 %s differs from %s", fileSHA, options.ExecutableSHA256)
	}
	host, _ := os.Hostname()
	run := &Run{SchemaVersion: 1, ID: options.ID, Commit: options.Commit, Version: options.Version, AssetSHA256: options.AssetSHA256,
		Executable: executable, ExecutableSHA256: fileSHA, Platform: options.Platform, Group: options.Group,
		PodmanConnection: options.PodmanConnection, Host: host, CreatedUTC: utcNow(), Root: root, Stdout: os.Stdout}
	if run.Platform == PlatformWindows && run.PodmanConnection == "" {
		connection, err := defaultPodmanMachine()
		if err != nil {
			return nil, err
		}
		run.PodmanConnection = connection
	}
	if _, err := Step(dir, "version", nil, exec.Command(executable, "version")); err != nil {
		return nil, err
	}

	// 2. The controller group reaches a child process and `list` shows only this checklist's sandboxes.
	if err := run.GroupProof(filepath.Join(dir, "group-proof.txt")); err != nil {
		return nil, err
	}

	// 3. Safe host diagnostics: no secrets, no private environment.
	var text strings.Builder
	fmt.Fprintf(&text, "utc=%s\n", utcNow())
	diagnostics := append([]*exec.Cmd{
		exec.Command("podman", "--version"),
		run.Podman("info", "--format", "rootless={{.Host.Security.Rootless}} graphroot={{.Store.GraphRoot}} cgroup={{.Host.CgroupVersion}} driver={{.Store.GraphDriverName}}"),
		run.Podman("system", "df"),
		run.SA("check"),
	}, hostDiagnostics()...)
	for _, command := range diagnostics {
		output, err := command.CombinedOutput()
		fmt.Fprintf(&text, "$ %s\n%s", strings.Join(command.Args, " "), output)
		if err != nil {
			fmt.Fprintf(&text, "error: %v\n", err)
		}
		text.WriteString("\n")
	}
	if err := writePrivate(filepath.Join(dir, "host.txt"), text.String()); err != nil {
		return nil, err
	}

	// 4. The whole-engine inventory.
	if err := run.ImageInventory(filepath.Join(dir, "images-baseline.txt")); err != nil {
		return nil, err
	}
	volumes, err := run.PodmanOutput("volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	if err := writePrivate(filepath.Join(dir, "volumes-baseline.txt"), sortedLines(volumes)); err != nil {
		return nil, err
	}
	images, err := ImageIDs(filepath.Join(dir, "images-baseline.txt"))
	if err != nil {
		return nil, err
	}

	// 5. The free space on the filesystem that holds Podman's storage.
	graphroot, err := run.PodmanOutput("info", "--format", "{{.Store.GraphRoot}}")
	if err != nil {
		return nil, err
	}
	free, err := freeSpaceOf(run, graphroot)
	if err != nil {
		return nil, err
	}
	containers, _ := run.PodmanOutput("ps", "--all", "--format", "{{.Names}}")
	summary := map[string]any{"utc": utcNow(), "graphroot": graphroot, "free_bytes": free, "min_free_bytes": options.MinFreeBytes,
		"images": len(images), "containers": len(strings.Fields(containers)), "volumes": len(strings.Fields(volumes))}
	if err := writePrivateJSON(filepath.Join(dir, "preflight.json"), summary); err != nil {
		return nil, err
	}
	if free < options.MinFreeBytes {
		return nil, fmt.Errorf("free space %d bytes on %s is below the required %d bytes", free, graphroot, options.MinFreeBytes)
	}

	// 6. The run's facts, written only now that every check passed, so a failed preflight leaves
	// no run behind and can be repeated with the same run ID.
	if err := run.Save(); err != nil {
		return nil, err
	}
	run.Printf("run %s verified: commit %s, version %s, asset-sha256 %s\n", run.ID, run.Commit, run.Version, run.AssetSHA256)
	run.Printf("executable %s (sha256 %s), group %s, platform %s\n", run.Executable, fileSHA, run.Group, run.Platform)
	run.Printf("inventory: %d images, %d containers, %d volumes; %d bytes free on %s\n", len(images), len(strings.Fields(containers)), len(strings.Fields(volumes)), free, graphroot)
	run.Printf("run directory: %s\n", root)
	return run, nil
}

// freeSpaceOf measures the free space that matters for Podman's storage: on Linux the filesystem
// of the graph root; on Windows the host drive that holds the machine, because the graph root is
// a path inside the machine.
func freeSpaceOf(run *Run, graphroot string) (uint64, error) {
	if run.Platform == PlatformWindows {
		home, err := os.UserHomeDir()
		if err != nil {
			return 0, err
		}
		return freeBytes(filepath.VolumeName(home) + `\`)
	}
	return freeBytes(graphroot)
}

// defaultPodmanMachine returns the default Podman machine, or the only machine, on Windows.
func defaultPodmanMachine() (string, error) {
	output, err := exec.Command("podman", "machine", "list", "--format", "{{.Name}} {{.Default}}").Output()
	if err != nil {
		return "", fmt.Errorf("podman machine list: %w", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "true" {
			return strings.TrimSuffix(fields[0], "*"), nil
		}
		if len(fields) >= 1 {
			names = append(names, strings.TrimSuffix(fields[0], "*"))
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", errors.New("no default Podman machine; pass the connection")
}
