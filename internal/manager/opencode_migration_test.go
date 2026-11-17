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
			effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
			output := runAgents(t, effects, 1, args...)
			for _, hint := range []string{"@opencode/cli", "not published", "agents update --unpin", "--version 2.3.0"} {
				if !strings.Contains(output, hint) {
					t.Fatalf("unavailable pin guidance lacks %q: %s", hint, output)
				}
			}
			if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "1.19.6" || got.Pin != "1.19.6" || installs != 0 || views != 1 {
				t.Fatalf("refused pin changed status=%+v installs=%d views=%d", got, installs, views)
			}
		})
	}
}

// TestOpenCodeFirstInstallPinGuidanceNamesEnable proves an unpublished first-install pin recommends an available enable version without an unusable unpin command at RunWithEffects.
func TestOpenCodeFirstInstallPinGuidanceNamesEnable(t *testing.T) {
	home, version, installs, views := t.TempDir(), "2.3.0", 0, 0
	effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
	output := runAgents(t, effects, 1, "enable", "opencode", "--version", "2.99.0")
	if !strings.Contains(output, "agents enable --version 2.3.0") || strings.Contains(output, "--unpin") {
		t.Fatalf("first install guidance=%s", output)
	}
	if got := readAgentStatus(t, effects, "opencode"); got.Enabled || installs != 0 || views != 1 {
		t.Fatalf("unavailable first-install pin changed status=%+v installs=%d views=%d", got, installs, views)
	}
}

// TestOpenCodePublishedPinAndUnpin proves a supported replacement pin, pinned update, and unpin migrate safely through RunWithEffects.
func TestOpenCodePublishedPinAndUnpin(t *testing.T) {
	home, version, installs, views := legacyOpenCodeHome(t, "1.19.6"), "2.3.0", 0, 0
	effects := openCodeRegistry(t, agentEffects(t, home, &version, &installs), openCodePublishedVersions, nil, &views)
	runAgents(t, effects, 0, "enable", "opencode", "--version", "2.0.0", "--force")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.0.0" || got.Pin != "2.0.0" || installs != 1 || views != 1 {
		t.Fatalf("replacement pin status=%+v installs=%d views=%d", got, installs, views)
	}
	runAgents(t, effects, 0, "update", "opencode")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.0.0" || got.Pin != "2.0.0" || installs != 2 || views != 2 {
		t.Fatalf("pinned update status=%+v installs=%d views=%d", got, installs, views)
	}
	runAgents(t, effects, 0, "update", "opencode", "--unpin")
	if got := readAgentStatus(t, effects, "opencode"); got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 3 || views != 2 {
		t.Fatalf("unpin status=%+v installs=%d views=%d", got, installs, views)
	}
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
		})
	}
}

// TestOpenCodeCutoverRejectsForeignMetadata proves the predecessor allowance cannot replace another package at RunWithEffects.
func TestOpenCodeCutoverRejectsForeignMetadata(t *testing.T) {
	home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
	metadata := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy", "lib", "node_modules", "opencode-ai", "package.json")
	if err := os.WriteFile(metadata, []byte(`{"name":"foreign-package","version":"1.19.6"}`), 0600); err != nil {
		t.Fatal(err)
	}
	e := agentEffects(t, home, &version, &installs)
	if output := runAgents(t, e, 1, "enable", "opencode", "--force"); !strings.Contains(output, "another package") {
		t.Fatalf("foreign package refusal=%s", output)
	}
	if got := readAgentStatus(t, e, "opencode"); got.InstalledVersion != "1.19.6" || installs != 0 {
		t.Fatalf("foreign package changed: status=%+v installs=%d", got, installs)
	}
}

// TestOpenCodePackageCutoverPreservesHome proves update and forced enable migrate a legacy installation while preserving configuration and credentials at RunWithEffects.
func TestOpenCodePackageCutoverPreservesHome(t *testing.T) {
	for _, args := range [][]string{{"update", "opencode"}, {"enable", "opencode", "--force"}} {
		t.Run(args[0], func(t *testing.T) {
			home, version, installs := legacyOpenCodeHome(t, ""), "2.3.0", 0
			preserved := map[string][]byte{}
			for _, relative := range []string{".config/opencode/opencode.json", ".local/share/opencode/auth.json"} {
				path := filepath.Join(home, relative)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				preserved[path] = data
			}
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, args...)
			if got := readAgentStatus(t, e, "opencode"); got.InstalledVersion != "2.3.0" || got.Pin != "" || installs != 1 {
				t.Fatalf("cutover status=%+v installs=%d", got, installs)
			}
			for path, want := range preserved {
				if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
					t.Fatalf("cutover changed %s: %q, error=%v", path, got, err)
				}
			}
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-legacy")); !os.IsNotExist(err) {
				t.Fatalf("legacy prefix retained: %v", err)
			}
		})
	}
}
