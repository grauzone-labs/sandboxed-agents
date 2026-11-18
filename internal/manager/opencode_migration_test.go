package manager_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// openCodePublishedVersions is the registry snapshot used by the pin command fixtures.
const openCodePublishedVersions = `{"versions":["2.0.0","2.3.0"],"dist-tags.latest":"2.3.0"}`

// legacyOpenCodeHome creates the installation and home data written before the OpenCode package cutover.
func legacyOpenCodeHome(t *testing.T, pin string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("agent installation tests require the Linux manager lock")
	}
	home := t.TempDir()
	root := filepath.Join(home, ".local", "share", "sandboxed-agents")
	prefix := filepath.Join(root, "installation-legacy")
	files := map[string]string{
		filepath.Join(prefix, "lib", "node_modules", "opencode-ai", "package.json"): `{"name":"opencode-ai","version":"1.19.6"}`,
		filepath.Join(prefix, "lib", "node_modules", "opencode-ai", "cli.js"):       "legacy command",
		filepath.Join(home, ".config", "opencode", "opencode.json"):                 `{"model":"cliproxyapi/test","provider":{"cliproxyapi":{"options":{"apiKey":"test-key"}}}}`,
		filepath.Join(home, ".local", "share", "opencode", "auth.json"):             `{"provider":{"key":"test-token"}}`,
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, bin := range []string{filepath.Join(prefix, "bin"), filepath.Join(home, ".local", "bin")} {
		if err := os.MkdirAll(bin, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(prefix, "bin", "opencode")
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", "opencode-ai", "cli.js"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".local", "bin", "opencode")); err != nil {
		t.Fatal(err)
	}
	selection, err := json.Marshal(map[string]any{"opencode": map[string]string{"version": "1.19.6", "pin": pin, "prefix": "installation-legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "selection.json"), selection, 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

// openCodeRegistry supplies a published package snapshot at the npm process boundary while real manager behavior handles it.
func openCodeRegistry(t *testing.T, effects manager.Effects, snapshot string, lookupError error, views *int) manager.Effects {
	t.Helper()
	start := effects.Start
	effects.Start = func(process manager.Process) error {
		if process.Program != "npm" || len(process.Args) == 0 || process.Args[0] != "view" {
			return start(process)
		}
		*views++
		if strings.Join(process.Args, " ") != "view --json -- @opencode/cli versions dist-tags.latest" || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) || process.EnvironmentPolicy != manager.NPMEnvironment {
			t.Fatalf("registry query=%+v", process)
		}
		fmt.Fprint(process.Stdout, snapshot)
		return lookupError
	}
	return effects
}

// snapshotOpenCodeFiles returns an assertion that the named home files retain their exact bytes.
func snapshotOpenCodeFiles(t *testing.T, home string, paths ...string) func() {
	t.Helper()
	before := make(map[string][]byte)
	for _, relative := range paths {
		path := filepath.Join(home, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	return func() {
		t.Helper()
		for path, want := range before {
			if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
				t.Fatalf("changed %s: %q, error=%v", path, got, err)
			}
		}
	}
}

// snapshotOpenCodeCommand returns an assertion that the managed link and its usable command retain their targets and bytes.
func snapshotOpenCodeCommand(t *testing.T, home string) func() {
	t.Helper()
	command := filepath.Join(home, ".local", "bin", "opencode")
	target, err := os.Readlink(command)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(command)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if got, err := os.Readlink(command); err != nil || got != target {
			t.Fatalf("command link changed=%q want=%q error=%v", got, target, err)
		}
		if got, err := filepath.EvalSymlinks(command); err != nil || got != resolved {
			t.Fatalf("resolved command changed=%q want=%q error=%v", got, resolved, err)
		}
		if got, err := os.ReadFile(command); err != nil || string(got) != string(content) {
			t.Fatalf("command changed=%q error=%v", got, err)
		}
	}
}

// snapshotOpenCodeState returns an assertion that refusal preserves selection, configuration, credentials, installations, and the usable command.
func snapshotOpenCodeState(t *testing.T, home string) func() {
	t.Helper()
	unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
	unchangedCommand := snapshotOpenCodeCommand(t, home)
	pattern := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-*")
	prefixes, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		unchangedFiles()
		unchangedCommand()
		if got, err := filepath.Glob(pattern); err != nil || strings.Join(got, "\n") != strings.Join(prefixes, "\n") {
			t.Fatalf("installation prefixes changed=%q want=%q error=%v", got, prefixes, err)
		}
	}
}

// assertOpenCodeSelectedCommand verifies the new selection publishes a regular command and current package within its prefix.
func assertOpenCodeSelectedCommand(t *testing.T, home string) {
	t.Helper()
	root := filepath.Join(home, ".local", "share", "sandboxed-agents")
	data, err := os.ReadFile(filepath.Join(root, "selection.json"))
	if err != nil {
		t.Fatal(err)
	}
	var selection map[string]struct{ Prefix string }
	if err := json.Unmarshal(data, &selection); err != nil {
		t.Fatal(err)
	}
	prefix := selection["opencode"].Prefix
	if prefix == "installation-legacy" || !strings.HasPrefix(prefix, "installation-") || filepath.Base(prefix) != prefix {
		t.Fatalf("migration retained an old or invalid selection prefix=%q", prefix)
	}
	command := filepath.Join(home, ".local", "bin", "opencode")
	want := filepath.Join(root, prefix, "bin", "opencode")
	if got, err := os.Readlink(command); err != nil || got != want {
		t.Fatalf("command target=%q want=%q error=%v", got, want, err)
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Join(root, prefix), resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		t.Fatalf("command resolves outside new selection=%q relative=%q error=%v", resolved, relative, err)
	}
	if info, err := os.Stat(command); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("selected command is not a regular file: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, prefix, "lib", "node_modules", "@opencode", "cli", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct{ Name string }
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Name != "@opencode/cli" {
		t.Fatalf("selected package=%q error=%v", metadata.Name, err)
	}
}

// TestOpenCodeSameVersionPinMigratesPredecessor proves an overlapping published version replaces the predecessor instead of only recording a pin at RunWithEffects.
func TestOpenCodeSameVersionPinMigratesPredecessor(t *testing.T) {
	home, version, installs, views := legacyOpenCodeHome(t, ""), "2.3.0", 0, 0
	unchanged := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
	effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), `{"versions":["1.19.6","2.3.0"],"dist-tags.latest":"2.3.0"}`, nil, &views)
	runAgents(t, effects, 0, "enable", "opencode", "--version", "1.19.6")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "1.19.6" || installs != 1 || views != 1 {
		t.Fatalf("overlapping pin status=%+v installs=%d views=%d", got, installs, views)
	}
	assertOpenCodeSelectedCommand(t, home)
	unchanged()
}

// TestOpenCodeUnavailablePinsPreserveInstallation proves recorded and requested unavailable pins refuse before npm install with actionable recovery guidance at RunWithEffects.
func TestOpenCodeUnavailablePinsPreserveInstallation(t *testing.T) {
	for _, args := range [][]string{
		{"update", "opencode"},
		{"enable", "opencode", "--force"},
		{"enable", "opencode", "--version", "1.19.6"},
		{"enable", "opencode", "--version", "2.99.0", "--force"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
			unchanged := snapshotOpenCodeState(t, home)
			effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
			output := runAgents(t, effects, 1, args...)
			for _, hint := range []string{"@opencode/cli", "not published", "agents update NAME opencode --unpin", "agents enable NAME opencode --version 2.3.0"} {
				if !strings.Contains(output, hint) {
					t.Fatalf("unavailable pin guidance lacks %q: %s", hint, output)
				}
			}
			if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "1.19.6" || installs != 0 || views != 1 {
				t.Fatalf("refused pin changed status=%+v installs=%d views=%d", got, installs, views)
			}
			unchanged()
		})
	}
}

// TestOpenCodeFirstInstallPinGuidanceNamesEnable proves an unpublished first-install pin recommends an available enable version without an unusable unpin command at RunWithEffects.
func TestOpenCodeFirstInstallPinGuidanceNamesEnable(t *testing.T) {
	home, version, installs, views := t.TempDir(), "2.3.0", 0, 0
	effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
	output := runAgents(t, effects, 1, "enable", "opencode", "--version", "2.99.0")
	if !strings.Contains(output, "agents enable NAME opencode --version 2.3.0") || strings.Contains(output, "--unpin") {
		t.Fatalf("first install guidance=%s", output)
	}
	if got := readAgentStatus(t, effects, "opencode"); got.Enabled || installs != 0 || views != 1 {
		t.Fatalf("unavailable first-install pin changed status=%+v installs=%d views=%d", got, installs, views)
	}
	if prefixes, err := filepath.Glob(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-*")); err != nil || len(prefixes) != 0 {
		t.Fatalf("refused first install left prefixes=%q error=%v", prefixes, err)
	}
}

// TestOpenCodePublishedPinAndUnpin proves a supported replacement pin, pinned update, and unpin migrate safely through RunWithEffects.
func TestOpenCodePublishedPinAndUnpin(t *testing.T) {
	home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
	unchanged := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
	effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
	runAgents(t, effects, 0, "enable", "opencode", "--version", "2.0.0", "--force")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.0.0" || got.Pin != "2.0.0" || installs != 1 || views != 1 {
		t.Fatalf("replacement pin status=%+v installs=%d views=%d", got, installs, views)
	}
	assertOpenCodeSelectedCommand(t, home)
	unchanged()
	runAgents(t, effects, 0, "update", "opencode")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.0.0" || got.Pin != "2.0.0" || installs != 2 || views != 2 {
		t.Fatalf("pinned update status=%+v installs=%d views=%d", got, installs, views)
	}
	assertOpenCodeSelectedCommand(t, home)
	unchanged()
	runAgents(t, effects, 0, "update", "opencode", "--unpin")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 3 || views != 2 {
		t.Fatalf("unpin status=%+v installs=%d views=%d", got, installs, views)
	}
	assertOpenCodeSelectedCommand(t, home)
	unchanged()
}

// TestOpenCodeHealthyEnableSkipsRegistry proves unchanged legacy and current selections remain usable offline at RunWithEffects.
func TestOpenCodeHealthyEnableSkipsRegistry(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		t.Run(fmt.Sprintf("migrated=%v", migrated), func(t *testing.T) {
			home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
			effects := agentEffects(t, home, &version, &installs)
			if migrated {
				published := openCodeRegistry(t, effects, openCodePublishedVersions, nil, &views)
				runAgents(t, published, 0, "enable", "opencode", "--version", "2.3.0", "--force")
			}
			before := readAgentStatus(t, effects, "opencode")
			unchanged := snapshotOpenCodeState(t, home)
			previousInstalls := installs
			views = 0
			offline := openCodeRegistry(t, effects, "", fmt.Errorf("registry offline"), &views)
			runAgents(t, offline, 0, "enable", "opencode")
			if migrated {
				runAgents(t, offline, 0, "enable", "opencode", "--force")
			}
			if after := readAgentStatus(t, effects, "opencode"); after != before || installs != previousInstalls || views != 0 {
				t.Fatalf("unchanged enable: before=%+v after=%+v installs=%d views=%d", before, after, installs, views)
			}
			unchanged()
		})
	}
}

// TestOpenCodeRepeatedPinChecksPublication proves explicitly repeating the current pin checks publication and refuses lookup failures before mutation at RunWithEffects.
func TestOpenCodeRepeatedPinChecksPublication(t *testing.T) {
	for _, test := range []struct {
		name, snapshot, diagnostic string
		err                        error
		status                     int
	}{
		{name: "published", snapshot: openCodePublishedVersions},
		{name: "offline", err: fmt.Errorf("registry offline"), diagnostic: "cannot check opencode pin 2.0.0", status: 1},
		{name: "unpublished", snapshot: `{"versions":["2.3.0"],"dist-tags.latest":"2.3.0"}`, diagnostic: "version 2.0.0 is not published", status: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, version, installs, views := legacyOpenCodeHome(t, ""), "2.3.0", 0, 0
			effects := agentEffects(t, home, &version, &installs)
			runAgents(t, openCodeRegistry(t, effects, openCodePublishedVersions, nil, &views), 0, "enable", "opencode", "--version", "2.0.0", "--force")
			unchanged := snapshotOpenCodeState(t, home)
			start := effects.Start
			effects.Start = func(process manager.Process) error {
				if process.Program == "tmux" {
					t.Fatal("repeated pin reached session mutation")
				}
				return start(process)
			}
			views = 0
			output := runAgents(t, openCodeRegistry(t, effects, test.snapshot, test.err, &views), test.status, "enable", "opencode", "--version", "2.0.0")
			if !strings.Contains(output, test.diagnostic) || installs != 1 || views != 1 {
				t.Fatalf("repeated pin output=%s installs=%d views=%d", output, installs, views)
			}
			unchanged()
		})
	}
}

// TestOpenCodeRegistryFailurePreservesSelection proves failed or malformed registry lookups stop before installs and session changes at RunWithEffects.
func TestOpenCodeRegistryFailurePreservesSelection(t *testing.T) {
	for _, test := range []struct {
		name, snapshot string
		err            error
	}{
		{"unreachable", "", fmt.Errorf("registry unreachable")},
		{"invalid-json", "not JSON", nil},
		{"missing-versions", `{}`, nil},
		{"missing-latest", `{"versions":["2.0.0"]}`, nil},
		{"unpublished-latest", `{"versions":["2.0.0"],"dist-tags.latest":"2.3.0"}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
			unchanged := snapshotOpenCodeState(t, home)
			effects := agentEffects(t, home, &version, &installs)
			start := effects.Start
			effects.Start = func(process manager.Process) error {
				if process.Program == "tmux" {
					t.Fatal("failed pin lookup reached session handling")
				}
				return start(process)
			}
			effects = openCodeRegistry(t, effects, test.snapshot, test.err, &views)
			output := runAgents(t, effects, 1, "enable", "opencode", "--force")
			if !strings.Contains(output, "cannot check opencode pin 1.19.6") || strings.Contains(output, "is not published") {
				t.Fatalf("lookup failure misreported as unavailable pin: %s", output)
			}
			if got := readAgentStatus(t, agentEffects(t, home, &version, &installs), "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "1.19.6" || installs != 0 || views != 1 {
				t.Fatalf("lookup failure changed status=%+v installs=%d views=%d", got, installs, views)
			}
			unchanged()
		})
	}
}

// TestOpenCodeCutoverRejectsForeignMetadata proves forced enable and ordinary update refuse a foreign selected package before replacement at RunWithEffects.
func TestOpenCodeCutoverRejectsForeignMetadata(t *testing.T) {
	for _, args := range [][]string{{"enable", "opencode", "--force"}, {"update", "opencode"}} {
		t.Run(args[0], func(t *testing.T) {
			home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
			metadata := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy", "lib", "node_modules", "opencode-ai", "package.json")
			if err := os.WriteFile(metadata, []byte(`{"name":"foreign-package","version":"1.19.6"}`), 0600); err != nil {
				t.Fatal(err)
			}
			unchanged := snapshotOpenCodeState(t, home)
			e := agentEffects(t, home, &version, &installs)
			if output := runAgents(t, e, 1, args...); !strings.Contains(output, "another package") {
				t.Fatalf("foreign package refusal=%s", output)
			}
			if got := readAgentStatus(t, e, "opencode"); got.InstalledVersion != "1.19.6" || installs != 0 {
				t.Fatalf("foreign package changed: status=%+v installs=%d", got, installs)
			}
			unchanged()
		})
	}
}

// TestOpenCodeDanglingCommandRequiresManualRecovery proves a missing inner npm target refuses mutations with explicit manual recovery guidance at RunWithEffects.
func TestOpenCodeDanglingCommandRequiresManualRecovery(t *testing.T) {
	for _, args := range [][]string{{"enable", "opencode", "--force"}, {"update", "opencode"}, {"disable", "opencode", "--force"}} {
		t.Run(args[0], func(t *testing.T) {
			home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
			command := filepath.Join(home, ".local", "bin", "opencode")
			previous, err := os.Readlink(command)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := filepath.EvalSymlinks(command)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(resolved); err != nil {
				t.Fatal(err)
			}
			unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json")
			effects := agentEffects(t, home, &version, &installs)
			output := runAgents(t, effects, 1, args...)
			if !strings.Contains(output, "manual recovery required") || !strings.Contains(output, "dangling command link") {
				t.Fatalf("dangling command guidance=%s", output)
			}
			if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "" || installs != 0 {
				t.Fatalf("dangling command changed status=%+v installs=%d", got, installs)
			}
			if got, err := os.Readlink(command); err != nil || got != previous {
				t.Fatalf("dangling command link changed=%q error=%v", got, err)
			}
			unchangedFiles()
		})
	}
}

// TestOpenCodeRecoveryRejectsForeignCommand proves recognized metadata alone cannot authorize a foreign command or deletion during forced mutations at RunWithEffects.
func TestOpenCodeRecoveryRejectsForeignCommand(t *testing.T) {
	for _, selected := range []bool{true, false} {
		for _, operation := range []string{"enable", "update", "disable"} {
			t.Run(fmt.Sprintf("selected=%v/%s", selected, operation), func(t *testing.T) {
				home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
				root := filepath.Join(home, ".local", "share", "sandboxed-agents")
				if !selected {
					if err := os.WriteFile(filepath.Join(root, "selection.json"), []byte(`{}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				target := filepath.Join(root, "installation-legacy", "bin", "opencode")
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("foreign command"), 0700); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "unrelated")
				if err := os.WriteFile(outside, []byte("outside prefix"), 0600); err != nil {
					t.Fatal(err)
				}
				unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json")
				unchangedCommand := snapshotOpenCodeCommand(t, home)
				effects := agentEffects(t, home, &version, &installs)
				output := runAgents(t, effects, 1, operation, "opencode", "--force")
				if selected || operation != "update" {
					if !strings.Contains(output, "command does not belong to the recognized package") {
						t.Fatalf("foreign command refusal=%s", output)
					}
				}
				if installs != 0 {
					t.Fatalf("foreign command started npm: %d", installs)
				}
				unchangedFiles()
				unchangedCommand()
				if got, err := os.ReadFile(outside); err != nil || string(got) != "outside prefix" {
					t.Fatalf("outside file changed=%q error=%v", got, err)
				}
			})
		}
	}
}

// TestOpenCodePackageCutoverPreservesHome proves update and forced enable migrate a legacy installation while preserving configuration and credentials at RunWithEffects.
func TestOpenCodePackageCutoverPreservesHome(t *testing.T) {
	for _, args := range [][]string{{"update", "opencode"}, {"enable", "opencode", "--force"}} {
		t.Run(args[0], func(t *testing.T) {
			home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
			unchanged := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, args...)
			if got := readAgentStatus(t, e, "opencode"); got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 1 {
				t.Fatalf("cutover status=%+v installs=%d", got, installs)
			}
			assertOpenCodeSelectedCommand(t, home)
			unchanged()
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy")); !os.IsNotExist(err) {
				t.Fatalf("legacy prefix retained: %v", err)
			}
		})
	}
}
