package manager_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// toolRegistry fakes the npm registry view of the Tool package: channel tags and published versions map to the version npm reports.
type toolRegistry map[string]string

// toolRun records what the fake processes saw and lets a test script the runtime prefetch failure modes.
type toolRun struct {
	views, installs, prefetches int
	installSpecs                []string
	prefetchEnvironments        [][]string
	// runtimeFailure is "", "exit", "no-marker", or "wrong-marker".
	runtimeFailure string
	installFailure bool
}

// toolDirectory is the Tool directory of T3 Code in a test home.
func toolDirectory(home string) string {
	return filepath.Join(home, ".local", "share", "sandboxed-agents", "tools", "t3code")
}

// toolEffects requires Linux for the real manager lock, then fakes npm and the launcher at the process seam, writing installation artifacts and the runtime marker into the test home.
func toolEffects(t *testing.T, home string, registry toolRegistry, run *toolRun) manager.Effects {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("tool installation tests require the Linux manager lock")
	}
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { return home, nil }
	e.Start = func(p manager.Process) error {
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("tool process not requested as agent: %+v", p)
		}
		if p.Program == "tmux" {
			return nil
		}
		if p.Program == "npm" {
			if p.EnvironmentPolicy != manager.NPMEnvironment {
				t.Fatalf("npm without the npm environment policy: %+v", p)
			}
			if len(p.Args) == 4 && p.Args[0] == "view" && p.Args[1] == "--" && p.Args[3] == "version" {
				run.views++
				spec := strings.TrimPrefix(p.Args[2], "t3@")
				if spec == p.Args[2] {
					t.Fatalf("view of another package: %q", p.Args)
				}
				version, published := registry[spec]
				if !published {
					fmt.Fprintf(p.Stderr, "npm error code E404\nnpm error 404 Not Found - GET https://registry.npmjs.org/t3/%s\n", spec)
					return childExit(1)
				}
				fmt.Fprintln(p.Stdout, version)
				return nil
			}
			if len(p.Args) == 6 && strings.Join(p.Args[:3], " ") == "install --global --prefix" && p.Args[4] == "--" {
				run.installs++
				run.installSpecs = append(run.installSpecs, p.Args[5])
				if run.installFailure {
					fmt.Fprintln(p.Stderr, "npm error network failure")
					return childExit(1)
				}
				prefix, version := p.Args[3], strings.TrimPrefix(p.Args[5], "t3@")
				packagePath := filepath.Join(prefix, "lib", "node_modules", "t3")
				if err := os.MkdirAll(packagePath, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(fmt.Sprintf(`{"name":"t3","version":%q}`, version)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
					t.Fatal(err)
				}
				return os.WriteFile(filepath.Join(prefix, "bin", "t3"), []byte("launcher"), 0700)
			}
			t.Fatalf("unexpected npm arguments %q", p.Args)
		}
		if filepath.Base(p.Program) == "t3" {
			if p.Program != filepath.Join(filepath.Dir(filepath.Dir(p.Program)), "bin", "t3") || !slices.Equal(p.Args, []string{"--version"}) {
				t.Fatalf("unexpected launcher call: %+v", p)
			}
			run.prefetches++
			run.prefetchEnvironments = append(run.prefetchEnvironments, p.Environment)
			version := strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(p.Program))), ".next-")
			if filepath.Base(filepath.Dir(filepath.Dir(p.Program))) == version {
				t.Fatalf("launcher prefetch outside the staging prefix: %s", p.Program)
			}
			if !slices.Contains(p.Environment, "T3CODE_HOME="+filepath.Join(toolDirectory(home), "home")) {
				t.Fatalf("launcher without T3CODE_HOME in the Tool directory: %q", p.Environment)
			}
			switch run.runtimeFailure {
			case "exit":
				fmt.Fprintln(p.Stderr, "error while loading shared libraries: libatomic.so.1")
				return childExit(127)
			case "no-marker":
				fmt.Fprintf(p.Stdout, "t3 v%s\n", version)
				return nil
			}
			marker := filepath.Join(toolDirectory(home), "home", "runtime", "versions", version, ".install-complete")
			if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
				t.Fatal(err)
			}
			content := version
			if run.runtimeFailure == "wrong-marker" {
				content = "0.0.1"
			}
			if err := os.WriteFile(marker, []byte(content+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(p.Stdout, "t3 v%s\n", version)
			return nil
		}
		t.Fatalf("unexpected program %q with %q", p.Program, p.Args)
		return nil
	}
	return e
}

// runTools exercises the public manager boundary for a tools request and checks its exit status before returning the combined output.
func runTools(t *testing.T, e manager.Effects, want int, args ...string) string {
	t.Helper()
	var out, err bytes.Buffer
	status := manager.RunWithEffects(append([]string{"tools"}, args...), &out, &err, e)
	if status != want {
		t.Fatalf("args=%q status=%d want=%d stdout=%s stderr=%s", args, status, want, &out, &err)
	}
	return out.String() + err.String()
}

// readToolStatus decodes the status returned by the public manager boundary.
func readToolStatus(t *testing.T, e manager.Effects) manager.ToolStatus {
	t.Helper()
	var status manager.ToolStatus
	if err := json.Unmarshal([]byte(runTools(t, e, 0, "status", "t3code")), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

// toolVersions lists the launcher prefixes and runtime directories present in the Tool directory.
func toolVersions(t *testing.T, home string) (prefixes, runtimes []string) {
	t.Helper()
	read := func(path string) []string {
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	return read(filepath.Join(toolDirectory(home), "npm")), read(filepath.Join(toolDirectory(home), "home", "runtime", "versions"))
}

// commandLink reads the Tool's command link in the agent's bin directory, or an empty string when it is absent.
func commandLink(t *testing.T, home string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(home, ".local", "bin", "t3"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// TestToolsEnableResolvesChannelAndCommitsSelection proves a first enable without a selector resolves the latest channel to one exact version, installs exactly that version, prefetches its runtime, links the command, records channel latest without a pin, and that a repeated enable changes nothing at RunWithEffects with a real temporary home.
func TestToolsEnableResolvesChannelAndCommitsSelection(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e := toolEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	if status := readToolStatus(t, e); status.Enabled || status.Name != "t3code" {
		t.Fatalf("status before enable=%+v", status)
	}
	if out := runTools(t, e, 0, "enable", "t3code"); !strings.Contains(out, "t3code enabled: version 0.1.2 (channel latest)") {
		t.Fatalf("enable output=%s", out)
	}
	status := readToolStatus(t, e)
	want := manager.ToolStatus{Name: "t3code", Enabled: true, InstalledVersion: "0.1.2", Channel: "latest"}
	if status != want {
		t.Fatalf("status=%+v want=%+v", status, want)
	}
	if run.installs != 1 || run.prefetches != 1 || !slices.Equal(run.installSpecs, []string{"t3@0.1.2"}) {
		t.Fatalf("installs=%d prefetches=%d specs=%q", run.installs, run.prefetches, run.installSpecs)
	}
	if link := commandLink(t, home); link != filepath.Join(toolDirectory(home), "npm", "0.1.2", "bin", "t3") {
		t.Fatalf("command link=%q", link)
	}
	if out := runTools(t, toolEffects(t, home, toolRegistry{"latest": "0.2.0"}, run), 0, "enable", "t3code"); !strings.Contains(out, "version 0.1.2 (channel latest)") {
		t.Fatalf("repeated enable output=%s", out)
	}
	if run.installs != 1 || run.views != 1 || readToolStatus(t, e) != want {
		t.Fatalf("repeated enable changed the installation: installs=%d views=%d status=%+v", run.installs, run.views, readToolStatus(t, e))
	}
}

// TestToolsSelectorsSwitchAndUpdate proves pins, channel switches, updates within a channel, pinned reinstalls of a damaged installation, and the return from a pin to a channel each install only the exact version the recorded selection names at RunWithEffects with a real temporary home.
func TestToolsSelectorsSwitchAndUpdate(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	registry := toolRegistry{"latest": "0.1.2", "nightly": "0.1.3-nightly.20261023.3600", "0.1.1": "0.1.1", "0.1.2": "0.1.2", "0.1.3-nightly.20261023.3600": "0.1.3-nightly.20261023.3600"}
	e := toolEffects(t, home, registry, run)
	runTools(t, e, 0, "enable", "t3code", "--version", "0.1.1")
	if got := readToolStatus(t, e); got.Pin != "0.1.1" || got.Channel != "" || got.InstalledVersion != "0.1.1" {
		t.Fatalf("pinned enable=%+v", got)
	}
	views := run.views
	runTools(t, e, 0, "update", "t3code")
	if got := readToolStatus(t, e); got.Pin != "0.1.1" || got.InstalledVersion != "0.1.1" || run.installs != 1 || run.views != views {
		t.Fatalf("pinned update reinstalled or resolved a healthy pin: %+v installs=%d views=%d", got, run.installs, run.views-views)
	}
	marker := filepath.Join(toolDirectory(home), "home", "runtime", "versions", "0.1.1", ".install-complete")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	runTools(t, e, 0, "update", "t3code")
	if got := readToolStatus(t, e); got.Pin != "0.1.1" || run.installs != 2 || run.installSpecs[1] != "t3@0.1.1" {
		t.Fatalf("damaged pin was not reinstalled at its pin: %+v specs=%q", got, run.installSpecs)
	}
	if out := runTools(t, e, 0, "update", "t3code", "--channel", "nightly"); !strings.Contains(out, "(channel nightly)") {
		t.Fatalf("channel switch output=%s", out)
	}
	if got := readToolStatus(t, e); got.Pin != "" || got.Channel != "nightly" || got.InstalledVersion != "0.1.3-nightly.20261023.3600" {
		t.Fatalf("switch to channel=%+v", got)
	}
	registry["nightly"] = "0.1.3-nightly.20261024.3610"
	registry["0.1.3-nightly.20261024.3610"] = "0.1.3-nightly.20261024.3610"
	runTools(t, e, 0, "update", "t3code")
	if got := readToolStatus(t, e); got.Channel != "nightly" || got.InstalledVersion != "0.1.3-nightly.20261024.3610" {
		t.Fatalf("update within channel=%+v", got)
	}
	runTools(t, e, 0, "enable", "t3code", "--channel", "latest")
	if got := readToolStatus(t, e); got.Channel != "latest" || got.InstalledVersion != "0.1.2" {
		t.Fatalf("enable with a differing channel=%+v", got)
	}
	runTools(t, e, 0, "enable", "t3code", "--version", "0.1.2")
	installs := run.installs
	if got := readToolStatus(t, e); got.Pin != "0.1.2" || got.Channel != "" || got.InstalledVersion != "0.1.2" {
		t.Fatalf("pin of the installed version=%+v", got)
	}
	if run.installs != installs {
		t.Fatalf("pinning the installed version reinstalled it")
	}
	prefixes, runtimes := toolVersions(t, home)
	if !slices.Equal(prefixes, []string{"0.1.2"}) || !slices.Equal(runtimes, []string{"0.1.2"}) {
		t.Fatalf("previous versions kept: prefixes=%q runtimes=%q", prefixes, runtimes)
	}
	for _, spec := range run.installSpecs {
		if version := strings.TrimPrefix(spec, "t3@"); version == spec || catalog.ValidateVersion(version) != nil || registry[version] != version {
			t.Fatalf("install did not target a published exact version: %q", spec)
		}
	}
}

// TestToolsFailuresKeepPreviousInstallationAndSelection proves a failed npm install, a failed runtime download, a missing or wrong completion marker, and an unpublished exact version each exit non-zero naming the step and leave the installation, selection, command link, and version directories unchanged at RunWithEffects with a real temporary home.
func TestToolsFailuresKeepPreviousInstallationAndSelection(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	registry := toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2", "0.2.0": "0.2.0"}
	e := toolEffects(t, home, registry, run)
	failing := toolEffects(t, home, registry, run)
	failing.Start = func(manager.Process) error { return childExit(1) }
	if out := runTools(t, failing, 1, "enable", "t3code"); !strings.Contains(out, "cannot resolve t3code channel latest") {
		t.Fatalf("failed resolution output=%s", out)
	}
	if got := readToolStatus(t, e); got.Enabled {
		t.Fatalf("failed first enable enabled the Tool: %+v", got)
	}
	runTools(t, e, 0, "enable", "t3code")
	before := readToolStatus(t, e)
	link := commandLink(t, home)
	for _, tc := range []struct {
		name, message string
		prepare       func()
		args          []string
	}{
		{"npm-install", "t3code installation failed", func() { run.installFailure = true }, []string{"enable", "t3code", "--version", "0.2.0"}},
		{"runtime-exit", "t3code runtime download for 0.2.0 failed", func() { run.runtimeFailure = "exit" }, []string{"update", "t3code", "--version", "0.2.0"}},
		{"no-marker", "t3code runtime 0.2.0 is incomplete", func() { run.runtimeFailure = "no-marker" }, []string{"enable", "t3code", "--version", "0.2.0"}},
		{"wrong-marker", "t3code runtime 0.2.0 is incomplete", func() { run.runtimeFailure = "wrong-marker" }, []string{"update", "t3code", "--version", "0.2.0"}},
		{"unpublished-enable", "t3code version 9.9.9 is not published", func() {}, []string{"enable", "t3code", "--version", "9.9.9"}},
		{"unpublished-update", "t3code version 9.9.9 is not published", func() {}, []string{"update", "t3code", "--version", "9.9.9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installs := run.installs
			run.installFailure, run.runtimeFailure = false, ""
			tc.prepare()
			t.Cleanup(func() { run.installFailure, run.runtimeFailure = false, "" })
			if out := runTools(t, e, 1, tc.args...); !strings.Contains(out, tc.message) {
				t.Fatalf("output=%s want %q", out, tc.message)
			}
			if got := readToolStatus(t, e); got != before {
				t.Fatalf("selection changed: %+v want %+v", got, before)
			}
			if got := commandLink(t, home); got != link {
				t.Fatalf("command link changed: %q want %q", got, link)
			}
			prefixes, runtimes := toolVersions(t, home)
			if !slices.Equal(prefixes, []string{"0.1.2"}) || !slices.Equal(runtimes, []string{"0.1.2"}) {
				t.Fatalf("failed switch left files: prefixes=%q runtimes=%q", prefixes, runtimes)
			}
			if strings.HasPrefix(tc.name, "unpublished") && run.installs != installs {
				t.Fatalf("unpublished version reached npm install")
			}
		})
	}
}

// TestToolsRepairsKeepTheInstalledVersionOnFailure proves a same-version repair of a damaged installation installs through a staging prefix, so a failed npm install or runtime download keeps the installed launcher, command link, and selection, and a successful repair restores the link, at RunWithEffects with a real temporary home.
func TestToolsRepairsKeepTheInstalledVersionOnFailure(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e := toolEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2", "nightly": "0.1.3-nightly.1", "0.1.3-nightly.1": "0.1.3-nightly.1"}, run)
	runTools(t, e, 0, "enable", "t3code")
	launcher := filepath.Join(toolDirectory(home), "npm", "0.1.2", "bin", "t3")
	before := readToolStatus(t, e)
	marker := filepath.Join(toolDirectory(home), "home", "runtime", "versions", "0.1.2", ".install-complete")
	for _, failure := range []func(){func() { run.installFailure = true }, func() { run.runtimeFailure = "exit" }} {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		failure()
		views := run.views
		if out := runTools(t, e, 1, "enable", "t3code"); strings.Contains(out, "cannot resolve") {
			t.Fatalf("repair resolved the channel: %s", out)
		}
		run.installFailure, run.runtimeFailure = false, ""
		if _, err := os.Stat(launcher); err != nil {
			t.Fatalf("failed repair removed the installed launcher: %v", err)
		}
		if got := readToolStatus(t, e); got != before || commandLink(t, home) != launcher || run.views != views {
			t.Fatalf("failed repair changed state: status=%+v link=%q views=%d", got, commandLink(t, home), run.views-views)
		}
	}
	if out := runTools(t, e, 0, "enable", "t3code"); !strings.Contains(out, "version 0.1.2 (channel latest)") {
		t.Fatalf("repair output=%s", out)
	}
	if !slices.Contains(run.installSpecs, "t3@0.1.2") || run.installSpecs[len(run.installSpecs)-1] != "t3@0.1.2" || readToolStatus(t, e) != before {
		t.Fatalf("repair did not reinstall the recorded version: specs=%q status=%+v", run.installSpecs, readToolStatus(t, e))
	}
	if err := os.Remove(filepath.Join(home, ".local", "bin", "t3")); err != nil {
		t.Fatal(err)
	}
	runTools(t, e, 0, "update", "t3code")
	if commandLink(t, home) != launcher || readToolStatus(t, e) != before {
		t.Fatalf("update did not restore the missing command link: link=%q status=%+v", commandLink(t, home), readToolStatus(t, e))
	}
	prefixes, _ := toolVersions(t, home)
	if !slices.Equal(prefixes, []string{"0.1.2"}) {
		t.Fatalf("staging prefixes left behind: %q", prefixes)
	}
}

// TestToolsFailedSelectionCommitRestoresTheCommandLink proves a switch whose selection cannot be written restores the previous command link and keeps the previous selection and installation at RunWithEffects with a real temporary home.
func TestToolsFailedSelectionCommitRestoresTheCommandLink(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	home, run := t.TempDir(), &toolRun{}
	e := toolEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2", "0.2.0": "0.2.0"}, run)
	runTools(t, e, 0, "enable", "t3code")
	before, link := readToolStatus(t, e), commandLink(t, home)
	if err := os.Chmod(toolDirectory(home), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(toolDirectory(home), 0700) })
	runTools(t, e, 1, "enable", "t3code", "--version", "0.2.0")
	if got := readToolStatus(t, e); got != before || commandLink(t, home) != link {
		t.Fatalf("failed commit changed state: status=%+v link=%q", got, commandLink(t, home))
	}
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("previous launcher removed: %v", err)
	}
}

// TestToolsPruneLeftoverVersions proves an enable removes launcher prefixes and runtime directories that the selection does not name before and after its own work at RunWithEffects with a real temporary home.
func TestToolsPruneLeftoverVersions(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e := toolEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	for _, leftover := range []string{filepath.Join(toolDirectory(home), "npm", "9.9.9", "bin"), filepath.Join(toolDirectory(home), "home", "runtime", "versions", "9.9.9")} {
		if err := os.MkdirAll(leftover, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runTools(t, e, 0, "enable", "t3code")
	prefixes, runtimes := toolVersions(t, home)
	if !slices.Equal(prefixes, []string{"0.1.2"}) || !slices.Equal(runtimes, []string{"0.1.2"}) {
		t.Fatalf("leftovers kept: prefixes=%q runtimes=%q", prefixes, runtimes)
	}
}

// TestToolsRequestsValidateBeforeHomeOrProcesses proves unknown tools, invalid channels, invalid versions, conflicting selectors, and malformed requests fail with usage status before the manager reads home or starts a process at RunWithEffects.
func TestToolsRequestsValidateBeforeHomeOrProcesses(t *testing.T) {
	for _, identity := range []manager.Identity{{UID: 0, GID: 0}, {UID: 1000, GID: 1000}} {
		e := noProcesses(t, identity)
		e.HomeDirectory = func() (string, error) { t.Error("validation read home"); return "", nil }
		for _, operation := range []string{"enable", "update", "status"} {
			if output := runTools(t, e, 2, operation, "unknown"); !strings.Contains(output, `unknown tool "unknown": valid names: t3code`) {
				t.Fatalf("unknown tool output=%s", output)
			}
		}
		if output := runTools(t, e, 2, "enable", "t3code", "--channel", "preview"); !strings.Contains(output, `invalid --channel "preview": valid channels: latest, nightly`) {
			t.Fatalf("invalid channel output=%s", output)
		}
		if output := runTools(t, e, 2, "update", "t3code", "--channel", "latest", "--version", "0.1.2"); !strings.Contains(output, "--channel and --version conflict") {
			t.Fatalf("conflicting selectors output=%s", output)
		}
		for _, version := range []string{"", "latest", "^0.1.2", "0.1", "0.1.2+build.4", "0.1.2;id"} {
			runTools(t, e, 2, "enable", "t3code", "--version", version)
		}
		for _, args := range [][]string{{}, {"enable"}, {"status", "t3code", "--channel", "latest"}, {"enable", "t3code", "extra"}, {"delete", "t3code"}} {
			runTools(t, e, 2, args...)
		}
	}
}

// TestToolsAdministrationReexecutesBeforeHomeAccess proves root tools requests re-exec a 1000:1000 worker before accessing home through the manager OS-effect seam.
func TestToolsAdministrationReexecutesBeforeHomeAccess(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	worker := toolEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	root.HomeDirectory = func() (string, error) { t.Fatal("root accessed home"); return "", nil }
	workers := 0
	root.Reexec = func(p manager.Process) error {
		workers++
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || len(p.Args) < 3 || p.Args[0] != "__worker" || p.Args[1] != "tools" {
			t.Fatalf("worker request=%+v", p)
		}
		if status := manager.RunWithEffects(p.Args, p.Stdout, p.Stderr, worker); status != 0 {
			return childExit(status)
		}
		return nil
	}
	for _, args := range [][]string{{"enable", "t3code"}, {"status", "t3code"}, {"update", "t3code"}} {
		runTools(t, root, 0, args...)
	}
	if workers != 3 || run.installs != 1 {
		t.Fatalf("workers=%d installs=%d", workers, run.installs)
	}
	unsafe := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	unsafe.Groups = func() ([]int, error) { return []int{0}, nil }
	unsafe.HomeDirectory = func() (string, error) { t.Fatal("unsafe worker accessed home"); return "", nil }
	for _, args := range [][]string{{"enable", "t3code"}, {"status", "t3code"}} {
		var out bytes.Buffer
		if status := manager.RunWithEffects(append([]string{"__worker", "tools"}, args...), &out, &out, unsafe); status != 1 {
			t.Fatalf("unsafe worker status=%d output=%s", status, &out)
		}
	}
}

// TestToolsUpdateRequiresEnabledTool proves update on a Tool that is not enabled fails without resolving or installing anything at RunWithEffects.
func TestToolsUpdateRequiresEnabledTool(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e := toolEffects(t, home, toolRegistry{"latest": "0.1.2"}, run)
	if out := runTools(t, e, 1, "update", "t3code"); !strings.Contains(out, "t3code not enabled") {
		t.Fatalf("update output=%s", out)
	}
	if run.views != 0 || run.installs != 0 {
		t.Fatalf("update of a disabled Tool reached npm: views=%d installs=%d", run.views, run.installs)
	}
}

// TestToolAndAgentChangesShareTheManagerLock proves an agent change in progress refuses a tool change at once and a tool change in progress refuses an agent change, while tools status reads its snapshot without the lock, at RunWithEffects with a real manager lock.
func TestToolAndAgentChangesShareTheManagerLock(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	version, installs := "1.0.0", 0
	registry := toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}
	type result struct {
		status int
		output string
	}
	invoke := func(e manager.Effects, args []string, done chan<- result) {
		var out bytes.Buffer
		status := manager.RunWithEffects(args, &out, &out, e)
		done <- result{status, out.String()}
	}
	hold := func(e manager.Effects) (manager.Effects, chan struct{}, chan struct{}) {
		inside, release := make(chan struct{}), make(chan struct{})
		start := e.Start
		e.Start = func(p manager.Process) error {
			select {
			case <-inside:
			default:
				close(inside)
				<-release
			}
			return start(p)
		}
		return e, inside, release
	}
	for _, tc := range []struct {
		name          string
		holder, other []string
		holderEffects func() manager.Effects
		otherEffects  func() manager.Effects
	}{
		{"agent holds, tool refuses", []string{"agents", "enable", "codex"}, []string{"tools", "enable", "t3code"},
			func() manager.Effects { return agentEffects(t, home, &version, &installs) }, func() manager.Effects { return toolEffects(t, home, registry, run) }},
		{"tool holds, agent refuses", []string{"tools", "enable", "t3code"}, []string{"agents", "enable", "claude"},
			func() manager.Effects { return toolEffects(t, home, registry, run) }, func() manager.Effects { return agentEffects(t, home, &version, &installs) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holder, inside, release := hold(tc.holderEffects())
			holderDone := make(chan result, 1)
			go invoke(holder, tc.holder, holderDone)
			<-inside
			defer func() {
				close(release)
				if got := <-holderDone; got.status != 0 {
					t.Fatalf("holder=%+v", got)
				}
			}()
			otherDone := make(chan result, 1)
			go invoke(tc.otherEffects(), tc.other, otherDone)
			select {
			case got := <-otherDone:
				if got.status != 1 || !strings.Contains(got.output, "another agent or tool change is in progress; retry") {
					t.Fatalf("competing change=%+v", got)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("competing change waited for the lock instead of refusing")
			}
			statusDone := make(chan result, 1)
			go invoke(toolEffects(t, home, registry, run), []string{"tools", "status", "t3code"}, statusDone)
			select {
			case got := <-statusDone:
				if got.status != 0 {
					t.Fatalf("status during a change=%+v", got)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("tools status waited for the lock")
			}
		})
	}
}
