package manager

import (
	"bytes"
	"fmt"
	"io"
)

const (
	// managerVersionLine is what the version command prints; the manager is built without an
	// injected version, and the image's asset hash identifies its bytes.
	managerVersionLine = "sandboxed-agents-manager development"
	// versionsManifest is the manifest of the installed runtime and toolchain versions that the
	// image recipes write, which the host CLI shows for an image.
	versionsManifest = "/usr/local/share/sandboxed-agents/versions.txt"
	// azureRequirements is the pinned dependency list of the Azure DevOps extension, which the Azure
	// toolchain recipe copies into the image.
	azureRequirements = "/usr/local/share/sandboxed-agents/azure-devops-requirements.txt"
)

// versionProbe is one command whose output the manifest records, run only when its program is on PATH.
type versionProbe struct {
	program string
	args    []string
	// environment holds variables appended to the probe's environment, such as the configuration
	// directory of a tool that writes state on its first run.
	environment []string
	// required makes a missing program fail the manifest instead of being skipped.
	required bool
}

// versionProbes are the commands the manifest records, in order: the Debian packages, the Node
// runtime and npm, then every optional toolchain the image may hold.
var versionProbes = []versionProbe{
	{program: "dpkg-query", args: []string{"-W", "-f=${Package}=${Version}\n"}, required: true},
	{program: "node", args: []string{"--version"}, required: true},
	{program: "npm", args: []string{"--version"}, required: true},
}

// toolchainProbes are the optional toolchain commands; each runs only when its program is present.
var toolchainProbes = []versionProbe{
	{program: "dotnet", args: []string{"--list-sdks"}},
	{program: "playwright", args: []string{"--version"}},
	{program: "az", args: []string{"version"}, environment: []string{"HOME=/root", "AZURE_CONFIG_DIR=/root/.azure"}},
	{program: "az", args: []string{"extension", "list"}, environment: []string{"HOME=/root", "AZURE_CONFIG_DIR=/root/.azure"}},
	{program: "cc", args: []string{"--version"}},
}

// recordVersions writes the versions manifest during an image build, as root: the OS release, the
// Debian packages, Node and npm, the manager's own version line, and every present toolchain's
// version. The Azure probes record the extension requirements too and remove the state the CLI
// writes below root's home on its first run, so the image holds no root-owned Azure state.
func recordVersions(stdout, stderr io.Writer, effects Effects) int {
	if effects.Identity() != rootIdentity {
		fmt.Fprintln(stderr, "record-versions runs as root during an image build")
		return 1
	}
	if effects.LookPath == nil || effects.WriteFile == nil {
		fmt.Fprintln(stderr, "record-versions requires program lookup and file writes")
		return 1
	}
	var manifest bytes.Buffer
	release, err := effects.ReadFile("/etc/os-release")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	manifest.Write(release)
	probe := func(p versionProbe) error {
		process := Process{Program: p.program, Args: p.args, Identity: rootIdentity, Stdout: &manifest, Stderr: stderr, Environment: p.environment}
		if err := effects.Start(process); err != nil {
			return fmt.Errorf("%s %v: %w", p.program, p.args, err)
		}
		return nil
	}
	for _, p := range versionProbes {
		if err := probe(p); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintln(&manifest, managerVersionLine)
	azurePresent := false
	for _, p := range toolchainProbes {
		if _, err := effects.LookPath(p.program); err != nil {
			continue
		}
		if err := probe(p); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		azurePresent = azurePresent || p.program == "az"
	}
	if azurePresent {
		requirements, err := effects.ReadFile(azureRequirements)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		manifest.Write(requirements)
		if err := probe(versionProbe{program: "rm", args: []string{"-rf", "/root/.azure"}}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := effects.WriteFile(versionsManifest, manifest.Bytes(), 0644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
