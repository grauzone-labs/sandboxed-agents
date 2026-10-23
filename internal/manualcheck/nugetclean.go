package manualcheck

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NuGetCleanOptions name the preview's NuGet command package and the install root.
type NuGetCleanOptions struct {
	Package     string
	InstallRoot string
}

// nugetState is the private state of item `nuget-install-clean` across its stages.
type nugetState struct {
	Package     string            `json:"package"`
	PackageSHA  string            `json:"package_sha256"`
	InstallRoot string            `json:"install_root"`
	Tools       string            `json:"tools"`
	Snapshots   map[string]string `json:"snapshots"`
	Stages      []string          `json:"stages"`
	// InstallEntry is the Path entry the installer must add and remove: the default command
	// directory below %LOCALAPPDATA%, recorded at the prepare stage.
	InstallEntry string `json:"install_entry"`
}

// defaultInstallEntry returns the directory the NuGet installer adds to the user Path, below
// LOCALAPPDATA (or AppData\Local below the home directory when the variable is unset).
func defaultInstallEntry(getenv func(string) string, home string) string {
	return filepath.Join(stateDirectoryFor("windows", getenv, home), "Programs", "sandboxed-agents")
}

// pathEntryMatches tells whether a raw Path entry names the expected directory, compared without
// regard to case as Windows resolves paths; the bytes themselves are compared elsewhere.
func pathEntryMatches(entry, expected string) bool {
	return strings.EqualFold(strings.TrimRight(entry, `\`), strings.TrimRight(expected, `\`))
}

// processElevated reports whether this process runs with an elevated, administrator token. The
// Windows build binds it to the token's elevation; elsewhere, and whenever the query fails, it
// returns an error, so that a stage which must run without administrator rights refuses instead of
// assuming.
var processElevated = func() (bool, error) {
	return false, errors.New("the token elevation cannot be read on this platform")
}

// requireNotElevated refuses an elevated process, and a process whose elevation cannot be read,
// because the NuGet install must work for a user without administrator rights.
func requireNotElevated() error {
	elevated, err := processElevated()
	if err != nil {
		return fmt.Errorf("nuget-install-clean must run without administrator rights, and the token elevation could not be read: %w", err)
	}
	if elevated {
		return errors.New("nuget-install-clean must run without administrator rights; this process is elevated; use a normal terminal")
	}
	return nil
}

// userPath reads the user's stored Path value raw, without expanding environment names.
// On Windows it is the registry value HKCU\Environment\Path; elsewhere it is unavailable.
var userPath = func() (value, kind string, err error) {
	return "", "", errors.New("the stored user Path exists on Windows only")
}

// NuGetInstallClean runs one stage of item `nuget-install-clean` on Windows 11: `prepare`
// extracts and unblocks the package and records the stored user Path; `after-install`,
// `after-reinstall`, and `after-remove` run the installer or remover without administrator
// rights and compare the stored Path with the previous stage, entry by entry and unexpanded.
// Opening a new terminal and running `version` there is the person's step after the install.
func (r *Run) NuGetInstallClean(stage string, options NuGetCleanOptions) error {
	if r.Platform != PlatformWindows {
		return errors.New("nuget-install-clean runs on windows-11 only")
	}
	// The gate comes before the item directory and before any stage changes the host.
	if err := requireNotElevated(); err != nil {
		return err
	}
	dir, err := r.ItemDir("nuget-install-clean")
	if err != nil {
		return err
	}
	statePath := filepath.Join(dir, "state.json")
	var state nugetState
	failures := &Failures{run: r}
	if stage == "prepare" {
		if options.Package == "" {
			return errors.New("the .nupkg of the preview is required")
		}
		if _, err := os.Stat(statePath); err == nil {
			return errors.New("the item was already prepared for this run")
		}
		pkg, err := filepath.Abs(options.Package)
		if err != nil {
			return err
		}
		if filepath.Base(pkg) != "SandboxedAgents."+r.Version+".nupkg" {
			return fmt.Errorf("the package must be SandboxedAgents.%s.nupkg of the preview under test", r.Version)
		}
		sha, err := FileSHA256(pkg)
		if err != nil {
			return err
		}
		if options.InstallRoot == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			options.InstallRoot = filepath.Join(home, "chk-nuget")
		}
		if _, err := os.Stat(options.InstallRoot); err == nil {
			return fmt.Errorf("%s exists; start from a clean machine or remove it first", options.InstallRoot)
		}
		if err := os.MkdirAll(options.InstallRoot, 0700); err != nil {
			return err
		}
		extract := fmt.Sprintf(`Copy-Item -LiteralPath '%s' -Destination '%s'; Expand-Archive -LiteralPath '%s' -DestinationPath '%s'; Get-ChildItem -LiteralPath '%s' -Filter *.ps1 | Unblock-File`,
			pkg, filepath.Join(options.InstallRoot, "package.zip"), filepath.Join(options.InstallRoot, "package.zip"), filepath.Join(options.InstallRoot, "package"), filepath.Join(options.InstallRoot, "package", "tools"))
		result, err := Step(dir, "extract", nil, exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", extract))
		if err != nil {
			return err
		}
		if result.Exit != 0 {
			return fmt.Errorf("extracting the package exited %d; see %s", result.Exit, result.Log)
		}
		value, kind, err := userPath()
		if err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		state = nugetState{Package: pkg, PackageSHA: sha, InstallRoot: options.InstallRoot, Tools: filepath.Join(options.InstallRoot, "package", "tools"), Snapshots: map[string]string{"prepare": kind + "\n" + value}, Stages: []string{"prepare"}, InstallEntry: defaultInstallEntry(os.Getenv, home)}
		if err := writePrivateJSON(statePath, state); err != nil {
			return err
		}
		r.Printf("prepared: package sha256 %s extracted to %s; stored user Path recorded\n", sha, options.InstallRoot)
		return nil
	}
	if err := readJSON(statePath, &state); err != nil {
		return fmt.Errorf("run the prepare stage first: %w", err)
	}
	previous := state.Stages[len(state.Stages)-1]
	expected := map[string]string{"after-install": "prepare", "after-reinstall": "after-install", "after-remove": "after-reinstall"}[stage]
	if expected == "" || previous != expected {
		return fmt.Errorf("stage %s must follow %s; the last stage was %s", stage, expected, previous)
	}
	script := "install-command.ps1"
	if stage == "after-remove" {
		script = "remove-command.ps1"
	}
	result, err := Step(dir, stage, nil, exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-File", filepath.Join(state.Tools, script)))
	if err != nil {
		return err
	}
	if result.Exit != 0 {
		failures.Addf("%s exited %d", script, result.Exit)
	}
	value, kind, err := userPath()
	if err != nil {
		return err
	}
	state.Snapshots[stage] = kind + "\n" + value
	state.Stages = append(state.Stages, stage)
	if err := writePrivateJSON(statePath, state); err != nil {
		return err
	}
	beforeKind, beforeValue, _ := strings.Cut(state.Snapshots[previous], "\n")
	added, removed := comparePathEntries(beforeValue, value)
	if beforeKind != kind {
		failures.Addf("the stored Path changed its registry kind from %s to %s", beforeKind, kind)
	}
	switch stage {
	case "after-install":
		if len(added) != 1 || len(removed) != 0 || !pathEntryMatches(added[0], state.InstallEntry) {
			failures.Addf("the install must add exactly the install directory entry %s; added %v, removed %v", state.InstallEntry, added, removed)
		}
		if err := r.Checkpoint(dir, "Open a new terminal and run `sandboxed-agents version`. Expected: it prints the preview's version. Then run the after-reinstall stage."); err != nil {
			return err
		}
	case "after-reinstall":
		if len(added) != 0 || len(removed) != 0 {
			failures.Addf("the reinstall must leave the stored Path unchanged; added %v, removed %v", added, removed)
		}
	case "after-remove":
		if len(added) != 0 || len(removed) != 1 || !pathEntryMatches(removed[0], state.InstallEntry) {
			failures.Addf("the removal must remove exactly the install directory entry %s; added %v, removed %v", state.InstallEntry, added, removed)
		}
		prepareKind, prepareValue, _ := strings.Cut(state.Snapshots["prepare"], "\n")
		if prepareKind != kind || prepareValue != value {
			failures.Addf("the stored Path after removal is not byte-for-byte the one recorded before the install")
		}
	}
	return failures.Finish(dir, "nuget-install-clean ("+stage+")", fmt.Sprintf("%s ran without administrator rights (exit %d); stored Path entries: added %v, removed %v, existing entries byte-for-byte unchanged", script, result.Exit, added, removed))
}

// comparePathEntries compares two raw Path values entry by entry and returns the entries added
// and the entries removed. Entries are compared byte for byte, unexpanded; an entry whose bytes
// changed appears as one removed and one added entry.
func comparePathEntries(before, after string) (added, removed []string) {
	earlier, later := splitPath(before), splitPath(after)
	count := map[string]int{}
	for _, entry := range earlier {
		count[entry]--
	}
	for _, entry := range later {
		count[entry]++
	}
	for _, entry := range later {
		if count[entry] > 0 {
			added = append(added, entry)
			count[entry]--
		}
	}
	for _, entry := range earlier {
		if count[entry] < 0 {
			removed = append(removed, entry)
			count[entry]++
		}
	}
	return added, removed
}

// splitPath splits a raw Path value into its non-empty entries.
func splitPath(value string) []string {
	var entries []string
	for _, entry := range strings.Split(value, ";") {
		if entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}
