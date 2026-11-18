package manager_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// mismatchedLegacyOpenCodeHome gives a legacy selection a command pointing at a distinct, valid predecessor installation.
func mismatchedLegacyOpenCodeHome(t *testing.T, pin string) string {
	t.Helper()
	home := legacyOpenCodeHome(t, pin)
	linkLegacyOpenCodeRecovery(t, home)
	return home
}

// linkLegacyOpenCodeRecovery replaces the home command with a link to a distinct, valid predecessor prefix and returns that prefix.
func linkLegacyOpenCodeRecovery(t *testing.T, home string) string {
	t.Helper()
	orphanHome := legacyOpenCodeHome(t, "")
	root := filepath.Join(home, ".local", "share", "sandboxed-agents")
	orphanPrefix := filepath.Join(root, "installation-interrupted")
	if err := os.Rename(filepath.Join(orphanHome, ".local", "share", "sandboxed-agents", "installation-legacy"), orphanPrefix); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(home, ".local", "bin", "opencode")
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(orphanPrefix, "bin", "opencode"), command); err != nil {
		t.Fatal(err)
	}
	return orphanPrefix
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

// TestOpenCodePinRecoveryHintsRetainForce proves refused pins preserve recovery state and required force while keeping ordinary missing-link updates unforced at RunWithEffects.
func TestOpenCodePinRecoveryHintsRetainForce(t *testing.T) {
	for _, test := range []struct {
		state, retry, wantPin string
		wantViews             int
	}{
		{state: "mismatched", retry: "update", wantViews: 1},
		{state: "mismatched", retry: "enable", wantPin: "2.3.0", wantViews: 2},
		{state: "unselected", retry: "enable", wantPin: "2.3.0", wantViews: 2},
		{state: "missing", retry: "update", wantViews: 1},
		{state: "missing", retry: "enable", wantPin: "2.3.0", wantViews: 2},
	} {
		t.Run(test.state+"/"+test.retry, func(t *testing.T) {
			home, version, installs, views := mismatchedLegacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
			command := filepath.Join(home, ".local", "bin", "opencode")
			args := []string{"enable", "opencode", "--force", "--version", "2.99.0"}
			var unchangedCommand func()
			if test.state == "missing" {
				if err := os.Remove(command); err != nil {
					t.Fatal(err)
				}
				args = []string{"update", "opencode"}
				unchangedCommand = func() {
					if _, err := os.Lstat(command); !os.IsNotExist(err) {
						t.Fatalf("refused pin created a command: %v", err)
					}
				}
			} else {
				unchangedCommand = snapshotOpenCodeCommand(t, home)
			}
			if test.state == "unselected" {
				if err := os.WriteFile(filepath.Join(home, ".local", "share", "sandboxed-agents", "selection.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json", ".local/share/sandboxed-agents/installation-interrupted/lib/node_modules/opencode-ai/package.json")
			effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
			output := runAgents(t, effects, 1, args...)
			_, hints, found := strings.Cut(output, "; retry ")
			wantEnable := "agents enable NAME opencode --version 2.3.0 --force"
			wantHints := "agents update NAME opencode --unpin --force or " + wantEnable
			if test.state == "unselected" {
				wantHints = wantEnable
			} else if test.state == "missing" {
				wantHints = "agents update NAME opencode --unpin or agents enable NAME opencode --version 2.3.0 --force"
			}
			if !found || strings.TrimSpace(hints) != wantHints || installs != 0 || views != 1 {
				t.Fatalf("pin refusal=%s installs=%d views=%d", output, installs, views)
			}
			unchangedFiles()
			unchangedCommand()
			if prefixes, err := filepath.Glob(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-*")); err != nil || len(prefixes) != 2 {
				t.Fatalf("pin refusal changed installation prefixes=%q error=%v", prefixes, err)
			}
			retries := strings.Split(strings.TrimSpace(hints), " or ")
			for _, retry := range retries {
				fields := strings.Fields(retry)
				if fields[1] != test.retry {
					continue
				}
				// NAME belongs to the host grammar; the manager receives the operation and agent.
				retryArgs := append([]string{fields[1]}, fields[3:]...)
				if test.state == "missing" && test.retry == "update" {
					busy, queries := effects, 0
					start := effects.Start
					busy.Start = func(process manager.Process) error {
						if process.Program != "tmux" {
							return start(process)
						}
						queries++
						if queries != 1 || !slices.Equal(process.Args, []string{"-L", "sandboxed-agents", "-f", "/dev/null", "list-sessions", "-F", "#{session_name}"}) || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
							t.Fatalf("unforced retry attempted session mutation or repeated query: %+v", process)
						}
						fmt.Fprintln(process.Stdout, "opencode")
						return nil
					}
					if refusal := runAgents(t, busy, 1, retryArgs...); !strings.Contains(refusal, "agent session opencode is running") || queries != 1 || installs != 0 || views != 1 {
						t.Fatalf("unforced retry bypassed session guard: %s queries=%d installs=%d views=%d", refusal, queries, installs, views)
					}
					unchangedFiles()
					unchangedCommand()
				}
				runAgents(t, effects, 0, retryArgs...)
				if got := readAgentStatus(t, effects, "opencode"); !got.Enabled || got.InstalledVersion != "2.3.0" || got.Pin != test.wantPin || installs != 1 || views != test.wantViews {
					t.Fatalf("retry status=%+v installs=%d views=%d", got, installs, views)
				}
				assertOpenCodeSelectedCommand(t, home)
			}
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
			unchangedMetadata := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json")
			e := agentEffects(t, home, &version, &installs)
			if output := runAgents(t, e, 1, args...); !strings.Contains(output, "another package") {
				t.Fatalf("foreign package refusal=%s", output)
			}
			if got := readAgentStatus(t, e, "opencode"); got.InstalledVersion != "1.19.6" || installs != 0 {
				t.Fatalf("foreign package changed: status=%+v installs=%d", got, installs)
			}
			unchanged()
			unchangedMetadata()
		})
	}
}

// TestOpenCodeMutationsRejectUnownedSelectedPrefix proves a valid recovery link or absent outer link cannot authorize deletion of a foreign or escaping selection before effects at RunWithEffects.
func TestOpenCodeMutationsRejectUnownedSelectedPrefix(t *testing.T) {
	for _, kind := range []string{"foreign", "symlink"} {
		for _, missingLink := range []bool{false, true} {
			for _, args := range [][]string{{"enable", "opencode", "--force"}, {"update", "opencode", "--force"}, {"disable", "opencode", "--force"}, {"update", "opencode"}, {"disable", "opencode"}} {
				t.Run(fmt.Sprintf("%s/missing-link=%v/%s", kind, missingLink, strings.Join(args, " ")), func(t *testing.T) {
					home, version, installs, sessions := mismatchedLegacyOpenCodeHome(t, ""), "2.3.0", 0, 0
					root := filepath.Join(home, ".local", "share", "sandboxed-agents")
					prefix := filepath.Join(root, "installation-legacy")
					var selectedTarget string
					if kind == "foreign" {
						if err := os.WriteFile(filepath.Join(prefix, "lib", "node_modules", "opencode-ai", "package.json"), []byte(`{"name":"foreign-package","version":"1.19.6"}`), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						outside := filepath.Join(t.TempDir(), "installation-outside")
						if err := os.Rename(prefix, outside); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, prefix); err != nil {
							t.Fatal(err)
						}
						selectedTarget = outside
					}
					command := filepath.Join(home, ".local", "bin", "opencode")
					var unchangedCommand func()
					if missingLink {
						if err := os.Remove(command); err != nil {
							t.Fatal(err)
						}
						unchangedCommand = func() {
							if _, err := os.Lstat(command); !os.IsNotExist(err) {
								t.Fatalf("refusal created a command: %v", err)
							}
						}
					} else {
						unchangedCommand = snapshotOpenCodeCommand(t, home)
					}
					unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/cli.js", ".local/share/sandboxed-agents/installation-interrupted/lib/node_modules/opencode-ai/package.json", ".local/share/sandboxed-agents/installation-interrupted/lib/node_modules/opencode-ai/cli.js")
					effects := agentEffects(t, home, &version, &installs)
					start := effects.Start
					effects.Start = func(process manager.Process) error {
						if process.Program == "tmux" {
							sessions++
						}
						return start(process)
					}
					output := runAgents(t, effects, 1, args...)
					if !strings.Contains(output, "refusing selected installation") || installs != 0 || sessions != 0 {
						t.Fatalf("selected-prefix refusal=%s installs=%d sessions=%d", output, installs, sessions)
					}
					unchangedFiles()
					unchangedCommand()
					if kind == "symlink" {
						if target, err := os.Readlink(prefix); err != nil || target != selectedTarget {
							t.Fatalf("selected prefix link changed=%q want=%q error=%v", target, selectedTarget, err)
						}
					}
					if prefixes, err := filepath.Glob(filepath.Join(root, "installation-*")); err != nil || len(prefixes) != 2 {
						t.Fatalf("refusal changed installation prefixes=%q error=%v", prefixes, err)
					}
				})
			}
		}
	}
}

// TestOpenCodeRecoveryAcceptsOwnedOrAbsentSelectedPrefix proves current, predecessor, and absent selections, including metadata symlinks to regular files, remain repairable or removable at RunWithEffects.
func TestOpenCodeRecoveryAcceptsOwnedOrAbsentSelectedPrefix(t *testing.T) {
	for _, kind := range []string{"current", "predecessor", "absent"} {
		for _, missingLink := range []bool{false, true} {
			for _, operation := range []string{"enable", "update", "disable"} {
				t.Run(fmt.Sprintf("%s/missing-link=%v/%s", kind, missingLink, operation), func(t *testing.T) {
					home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
					effects := agentEffects(t, home, &version, &installs)
					if kind == "current" {
						runAgents(t, effects, 0, "enable", "opencode", "--force")
					}
					command := filepath.Join(home, ".local", "bin", "opencode")
					target, err := os.Readlink(command)
					if err != nil {
						t.Fatal(err)
					}
					selectedPrefix := filepath.Dir(filepath.Dir(target))
					oldPrefixes := []string{selectedPrefix}
					if kind == "absent" {
						if err := os.RemoveAll(selectedPrefix); err != nil {
							t.Fatal(err)
						}
					} else {
						pkg := "opencode-ai"
						if kind == "current" {
							pkg = "@opencode/cli"
						}
						metadata := filepath.Join(selectedPrefix, "lib", "node_modules", pkg, "package.json")
						if err := os.Rename(metadata, metadata+".saved"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink("package.json.saved", metadata); err != nil {
							t.Fatal(err)
						}
					}
					if missingLink {
						if err := os.Remove(command); err != nil {
							t.Fatal(err)
						}
					} else {
						oldPrefixes = append(oldPrefixes, linkLegacyOpenCodeRecovery(t, home))
					}
					unchanged := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
					installs = 0
					runAgents(t, effects, 0, operation, "opencode", "--force")
					got := readAgentStatus(t, effects, "opencode")
					if operation == "disable" {
						if got.Enabled || got.Pin != "" || installs != 0 {
							t.Fatalf("recovered disable status=%+v installs=%d", got, installs)
						}
						if _, err := os.Lstat(command); !os.IsNotExist(err) {
							t.Fatalf("disabled command retained: %v", err)
						}
					} else {
						if !got.Enabled || got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 1 {
							t.Fatalf("repaired installation status=%+v installs=%d", got, installs)
						}
						assertOpenCodeSelectedCommand(t, home)
					}
					for _, prefix := range oldPrefixes {
						if _, err := os.Lstat(prefix); !os.IsNotExist(err) {
							t.Fatalf("old prefix retained=%q error=%v", prefix, err)
						}
					}
					unchanged()
				})
			}
		}
	}
}

// TestOpenCodeManualRecoveryPreservesSessionGuards proves manual recovery keeps update and disable unforced after a dangling link is removed while enable and working mismatches require force at RunWithEffects.
func TestOpenCodeManualRecoveryPreservesSessionGuards(t *testing.T) {
	for _, workingLink := range []bool{false, true} {
		for _, operation := range []string{"update", "disable", "enable"} {
			t.Run(fmt.Sprintf("working-link=%v/%s", workingLink, operation), func(t *testing.T) {
				var home string
				if workingLink {
					home = mismatchedLegacyOpenCodeHome(t, "1.19.6")
				} else {
					home = legacyOpenCodeHome(t, "1.19.6")
				}
				prefix := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy")
				retainedFile := filepath.Join(prefix, "preserve.txt")
				if err := os.WriteFile(retainedFile, []byte("confirmed legacy files to preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"package.json", "cli.js"} {
					if err := os.Remove(filepath.Join(prefix, "lib", "node_modules", "opencode-ai", file)); err != nil {
						t.Fatal(err)
					}
				}
				command := filepath.Join(home, ".local", "bin", "opencode")
				previous, err := os.Readlink(command)
				if err != nil {
					t.Fatal(err)
				}
				unchanged := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/preserve.txt")
				args := []string{operation, "opencode"}
				if operation == "update" {
					args = append(args, "--unpin")
				} else if operation == "enable" {
					args = append(args, "--version", "2.3.0")
				}
				version, installs, views := "2.3.0", 0, 0
				effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
				output := runAgents(t, effects, 1, args...)
				wantRetry := "then retry the original command and options without adding --force; use --force only if the command-link repair check requires it or you intend to stop a running session"
				if operation == "enable" {
					wantRetry = "then retry with --force"
				}
				if !strings.Contains(output, wantRetry) || installs != 0 || views != 0 {
					t.Fatalf("manual recovery guidance=%s installs=%d views=%d", output, installs, views)
				}
				unchanged()
				if target, err := os.Readlink(command); err != nil || target != previous {
					t.Fatalf("refusal changed command=%q error=%v", target, err)
				}
				aside := filepath.Join(home, "preserved-opencode")
				if err := os.Rename(prefix, aside); err != nil {
					t.Fatal(err)
				}
				if !workingLink {
					if _, err := os.Stat(command); !os.IsNotExist(err) {
						t.Fatalf("command target was not confirmed missing: %v", err)
					}
					if err := os.Remove(command); err != nil {
						t.Fatal(err)
					}
				} else if _, err := os.Stat(command); err != nil {
					t.Fatal("working recovery command was lost:", err)
				}
				preserved := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json", "preserved-opencode/preserve.txt")
				selectionUnchanged := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json")
				if workingLink || operation == "enable" {
					wantForceHint := "opencode command is missing; retry agents enable with --force to repair it"
					if workingLink {
						wantForceHint = "opencode command differs from its selection; retry with --force to repair a managed installation"
					}
					if refusal := runAgents(t, effects, 1, args...); strings.TrimSpace(refusal) != wantForceHint || installs != 0 || views != 0 {
						t.Fatalf("link repair did not require force: %s installs=%d views=%d", refusal, installs, views)
					}
					args = append(args, "--force")
				} else {
					busy, queries := effects, 0
					start := effects.Start
					busy.Start = func(process manager.Process) error {
						if process.Program != "tmux" {
							return start(process)
						}
						queries++
						if queries != 1 || !slices.Equal(process.Args, []string{"-L", "sandboxed-agents", "-f", "/dev/null", "list-sessions", "-F", "#{session_name}"}) || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
							t.Fatalf("ordinary retry attempted session mutation or repeated query: %+v", process)
						}
						fmt.Fprintln(process.Stdout, "opencode")
						return nil
					}
					if refusal := runAgents(t, busy, 1, args...); !strings.Contains(refusal, "agent session opencode is running") || queries != 1 || installs != 0 || views != 0 {
						t.Fatalf("ordinary retry bypassed session guard: %s queries=%d installs=%d views=%d", refusal, queries, installs, views)
					}
				}
				selectionUnchanged()
				preserved()
				runAgents(t, effects, 0, args...)
				got := readAgentStatus(t, effects, "opencode")
				if operation == "disable" {
					if got.Enabled || got.Pin != "" || installs != 0 || views != 0 {
						t.Fatalf("manual disable status=%+v installs=%d views=%d", got, installs, views)
					}
					if _, err := os.Lstat(command); !os.IsNotExist(err) {
						t.Fatalf("manual disable left a command entry: %v", err)
					}
				} else {
					wantPin, wantViews := "", 0
					if operation == "enable" {
						wantPin, wantViews = "2.3.0", 1
					}
					if !got.Enabled || got.InstalledVersion != "2.3.0" || got.Pin != wantPin || installs != 1 || views != wantViews {
						t.Fatalf("manual repair status=%+v installs=%d views=%d", got, installs, views)
					}
					assertOpenCodeSelectedCommand(t, home)
				}
				if workingLink {
					if _, err := os.Lstat(filepath.Dir(filepath.Dir(previous))); !os.IsNotExist(err) {
						t.Fatalf("working recovery prefix was not cleaned: %v", err)
					}
				}
				if target, err := os.Readlink(filepath.Join(aside, "bin", "opencode")); err != nil || target != filepath.Join("..", "lib", "node_modules", "opencode-ai", "cli.js") {
					t.Fatalf("held-aside command changed=%q error=%v", target, err)
				}
				preserved()
			})
		}
	}
}

// TestOpenCodeDamagedSelectedPrefixRequiresManualRecovery proves missing metadata and executable refuse mutations with safe manual recovery guidance and preserve state at RunWithEffects.
func TestOpenCodeDamagedSelectedPrefixRequiresManualRecovery(t *testing.T) {
	for _, missingLink := range []bool{false, true} {
		for _, operation := range []string{"enable", "update", "disable"} {
			t.Run(fmt.Sprintf("missing-link=%v/%s", missingLink, operation), func(t *testing.T) {
				home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
				prefix := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy")
				info, err := os.Lstat(prefix)
				if err != nil {
					t.Fatal(err)
				}
				var removed []string
				for _, file := range []string{"package.json", "cli.js"} {
					path := filepath.Join(prefix, "lib", "node_modules", "opencode-ai", file)
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					removed = append(removed, path)
				}
				command := filepath.Join(home, ".local", "bin", "opencode")
				if missingLink {
					if err := os.Remove(command); err != nil {
						t.Fatal(err)
					}
				}
				unchanged := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
				output := runAgents(t, agentEffects(t, home, &version, &installs), 1, operation, "opencode", "--force")
				for _, hint := range []string{"inspect the selected prefix", "preserve its files", "move it aside only if confirmed to be a damaged managed installation", "remove the command link only if its target is confirmed missing", "leave a working link in place for the manager to validate and clean up", "retry with --force", "never delete foreign files"} {
					if !strings.Contains(output, hint) {
						t.Fatalf("damaged-prefix refusal lacks %q: %s", hint, output)
					}
				}
				if got, err := os.Lstat(prefix); err != nil || !os.SameFile(info, got) || !got.IsDir() || installs != 0 {
					t.Fatalf("damaged prefix changed or installed: error=%v installs=%d", err, installs)
				}
				for _, path := range removed {
					if _, err := os.Lstat(path); !os.IsNotExist(err) {
						t.Fatalf("refusal recreated %s: %v", path, err)
					}
				}
				if missingLink {
					if _, err := os.Lstat(command); !os.IsNotExist(err) {
						t.Fatalf("refusal created command: %v", err)
					}
				} else if target, err := os.Readlink(command); err != nil || target != filepath.Join(prefix, "bin", "opencode") {
					t.Fatalf("damaged command changed=%q error=%v", target, err)
				}
				if target, err := os.Readlink(filepath.Join(prefix, "bin", "opencode")); err != nil || target != filepath.Join("..", "lib", "node_modules", "opencode-ai", "cli.js") {
					t.Fatalf("inner command changed=%q error=%v", target, err)
				}
				unchanged()
			})
		}
	}
}

// TestOpenCodeDanglingCommandRequiresManualRecovery proves dangling-target refusals preserve state and operation-specific retries keep the ordinary update session guard at RunWithEffects.
func TestOpenCodeDanglingCommandRequiresManualRecovery(t *testing.T) {
	const ordinaryRetry = "retry the original command and options without adding --force; use --force only if the command-link repair check requires it or you intend to stop a running session"
	for _, test := range []struct {
		args      []string
		wantRetry string
	}{
		{[]string{"enable", "opencode"}, "retry with --force"},
		{[]string{"enable", "opencode", "--force"}, "retry with --force"},
		{[]string{"update", "opencode"}, ordinaryRetry},
		{[]string{"update", "opencode", "--force"}, "retry with --force"},
		{[]string{"disable", "opencode"}, ordinaryRetry},
		{[]string{"disable", "opencode", "--force"}, "retry with --force"},
	} {
		args := test.args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
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
			if !strings.Contains(output, test.wantRetry) {
				t.Fatalf("dangling command retry guidance=%s", output)
			}
			if !strings.Contains(output, "manual recovery required") || !strings.Contains(output, "remove the dangling command link only if its target is confirmed missing") {
				t.Fatalf("dangling command guidance=%s", output)
			}
			if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "" || installs != 0 {
				t.Fatalf("dangling command changed status=%+v installs=%d", got, installs)
			}
			if got, err := os.Readlink(command); err != nil || got != previous {
				t.Fatalf("dangling command link changed=%q error=%v", got, err)
			}
			unchangedFiles()
			if args[0] == "update" && len(args) == 2 {
				preserved := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
				if _, err := os.Stat(command); !os.IsNotExist(err) {
					t.Fatalf("dangling target was not confirmed missing: %v", err)
				}
				if err := os.Remove(command); err != nil {
					t.Fatal(err)
				}
				busy, queries := effects, 0
				start := effects.Start
				busy.Start = func(process manager.Process) error {
					if process.Program != "tmux" {
						return start(process)
					}
					queries++
					if queries != 1 || !slices.Equal(process.Args, []string{"-L", "sandboxed-agents", "-f", "/dev/null", "list-sessions", "-F", "#{session_name}"}) || process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
						t.Fatalf("ordinary dangling-link retry attempted session mutation: %+v", process)
					}
					fmt.Fprintln(process.Stdout, "opencode")
					return nil
				}
				if refusal := runAgents(t, busy, 1, args...); !strings.Contains(refusal, "agent session opencode is running") || queries != 1 || installs != 0 {
					t.Fatalf("ordinary dangling-link retry bypassed session guard: %s queries=%d installs=%d", refusal, queries, installs)
				}
				unchangedFiles()
				if _, err := os.Lstat(command); !os.IsNotExist(err) {
					t.Fatalf("session refusal created a command: %v", err)
				}
				runAgents(t, effects, 0, args...)
				if got := readAgentStatus(t, effects, "opencode"); !got.Enabled || got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 1 {
					t.Fatalf("ordinary dangling-link retry status=%+v installs=%d", got, installs)
				}
				assertOpenCodeSelectedCommand(t, home)
				preserved()
			}
		})
	}
}

// TestOpenCodeForcedEnableRecoversLegacyCommand proves force migrates mismatched and unselected predecessor links, cleans old prefixes, and preserves home data and requested pins at RunWithEffects.
func TestOpenCodeForcedEnableRecoversLegacyCommand(t *testing.T) {
	for _, test := range []struct {
		name                 string
		selected             bool
		args                 []string
		wantVersion, wantPin string
		wantViews            int
	}{
		{name: "mismatched-selected", selected: true, args: []string{"enable", "opencode", "--force", "--version", "2.0.0"}, wantVersion: "2.0.0", wantPin: "2.0.0", wantViews: 1},
		{name: "unselected", args: []string{"enable", "opencode", "--force"}, wantVersion: "2.3.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
			root := filepath.Join(home, ".local", "share", "sandboxed-agents")
			oldPrefixes := []string{filepath.Join(root, "installation-legacy")}
			if test.selected {
				oldPrefixes = append(oldPrefixes, linkLegacyOpenCodeRecovery(t, home))
			} else if err := os.WriteFile(filepath.Join(root, "selection.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			unchanged := snapshotOpenCodeFiles(t, home, ".config/opencode/opencode.json", ".local/share/opencode/auth.json")
			effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
			runAgents(t, effects, 0, test.args...)
			if got := readAgentStatus(t, effects, "opencode"); !got.Enabled || got.InstalledVersion != test.wantVersion || got.Pin != test.wantPin || installs != 1 || views != test.wantViews {
				t.Fatalf("legacy recovery status=%+v installs=%d views=%d", got, installs, views)
			}
			assertOpenCodeSelectedCommand(t, home)
			for _, prefix := range oldPrefixes {
				if _, err := os.Lstat(prefix); !os.IsNotExist(err) {
					t.Fatalf("unreferenced legacy prefix retained=%q error=%v", prefix, err)
				}
			}
			unchanged()
		})
	}
}

// TestOpenCodeRecoveryRejectsForeignCommand proves recognized metadata alone cannot authorize a foreign command or deletion during forced mutations at RunWithEffects.
func TestOpenCodeRecoveryRejectsForeignCommand(t *testing.T) {
	for _, selected := range []bool{true, false} {
		for _, operation := range []string{"enable", "update", "disable"} {
			if !selected && operation == "update" {
				continue
			}
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
				unchangedFiles := snapshotOpenCodeFiles(t, home, ".local/share/sandboxed-agents/selection.json", ".config/opencode/opencode.json", ".local/share/opencode/auth.json", ".local/share/sandboxed-agents/installation-legacy/lib/node_modules/opencode-ai/package.json")
				unchangedCommand := snapshotOpenCodeCommand(t, home)
				effects := agentEffects(t, home, &version, &installs)
				output := runAgents(t, effects, 1, operation, "opencode", "--force")
				if !strings.Contains(output, "command does not belong to the recognized package") {
					t.Fatalf("foreign command refusal=%s", output)
				}
				if installs != 0 {
					t.Fatalf("foreign command started npm: %d", installs)
				}
				unchangedFiles()
				unchangedCommand()
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
