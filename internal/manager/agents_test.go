package manager_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

func agentEffects(t *testing.T, home string, version *string, installs *int) manager.Effects {
	t.Helper()
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { return home, nil }
	e.Start = func(p manager.Process) error {
		if p.Program != "npm" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("unexpected installation process=%+v", p)
		}
		*installs++
		if len(p.Args) != 6 || strings.Join(p.Args[:3], " ") != "install --global --prefix" || p.Args[4] != "--" {
			t.Fatalf("npm arguments=%q", p.Args)
		}
		prefix, spec := p.Args[3], p.Args[5]
		packageName, installed := spec, *version
		if at := strings.LastIndex(spec, "@"); at > 0 {
			packageName, installed = spec[:at], spec[at+1:]
		}
		command := ""
		for _, a := range catalog.Default.Agents {
			if a.Install.Package == packageName {
				command = a.Command
			}
		}
		if command == "" {
			t.Fatalf("unknown npm package %q", packageName)
		}
		packagePath := filepath.Join(prefix, "lib", "node_modules", packageName)
		if err := os.MkdirAll(packagePath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(fmt.Sprintf(`{"version":%q}`, installed)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(filepath.Join(prefix, "bin", command), []byte("managed command"), 0700)
	}
	return e
}

func runAgents(t *testing.T, e manager.Effects, want int, args ...string) string {
	t.Helper()
	var out, err bytes.Buffer
	status := manager.RunWithEffects(append([]string{"agents"}, args...), &out, &err, e)
	if status != want {
		t.Fatalf("args=%q status=%d want=%d stdout=%s stderr=%s", args, status, want, &out, &err)
	}
	return out.String() + err.String()
}

// TestAgentsEnablePersistsInstalledVersion proves enable installs as agent and a fresh manager reads the installed version at RunWithEffects with a real temporary home.
func TestAgentsEnablePersistsInstalledVersion(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	var status map[string]any
	output := runAgents(t, agentEffects(t, home, &version, &installs), 0, "status", "codex")
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("status=%s error=%v", output, err)
	}
	if status["enabled"] != true || status["installed_version"] != "1.2.3" || installs != 1 {
		t.Fatalf("status=%v installs=%d", status, installs)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "codex")); err != nil {
		t.Fatalf("managed command unavailable: %v", err)
	}
}

// TestFourAgentsLifecyclePreservesHome proves the four catalog packages can enable, report, list, and disable without changing credentials or caches at RunWithEffects and the temporary-home filesystem seam.
func TestFourAgentsLifecyclePreservesHome(t *testing.T) {
	for _, name := range []string{"copilot", "claude", "codex", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home, version, installs := t.TempDir(), "2.3.4", 0
			for _, file := range []string{".git-credentials", ".npm/cache", ".config/agent/token", "notes.txt"} {
				path := filepath.Join(home, file)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("preserve "+file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", name)
			runAgents(t, e, 0, "enable", name, "--force")
			if installs != 1 {
				t.Fatalf("idempotent enable installed %d times", installs)
			}
			if out := runAgents(t, e, 0, "list"); out != fmt.Sprintf("[%q]\n", name) {
				t.Fatalf("list=%s", out)
			}
			runAgents(t, e, 0, "disable", name)
			runAgents(t, e, 0, "disable", name, "--force")
			if out := runAgents(t, e, 0, "list"); out != "[]\n" {
				t.Fatalf("disabled list=%s", out)
			}
			var status map[string]any
			if err := json.Unmarshal([]byte(runAgents(t, e, 0, "status", name)), &status); err != nil {
				t.Fatal(err)
			}
			if status["enabled"] != false || status["installed_version"] != "" || status["pin"] != "" {
				t.Fatalf("disabled status=%v", status)
			}
			command := filepath.Join(home, ".local", "bin", catalog.Default.Agents[name].Command)
			if _, err := os.Lstat(command); !os.IsNotExist(err) {
				t.Fatalf("command retained: %v", err)
			}
			for _, file := range []string{".git-credentials", ".npm/cache", ".config/agent/token", "notes.txt"} {
				if data, err := os.ReadFile(filepath.Join(home, file)); err != nil || string(data) != "preserve "+file {
					t.Fatalf("file %s changed=%q error=%v", file, data, err)
				}
			}
		})
	}
}

func readAgentStatus(t *testing.T, e manager.Effects, name string) manager.AgentStatus {
	t.Helper()
	var status manager.AgentStatus
	if err := json.Unmarshal([]byte(runAgents(t, e, 0, "status", name)), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestAgentPinsAndUpdates proves same-version pinning skips npm, changed pins install, pinned updates reinstall, unpin installs latest, and disable removes the pin at RunWithEffects with a temporary home.
func TestAgentPinsAndUpdates(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	runAgents(t, e, 0, "enable", "codex", "--version", "1.2.3")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "1.2.3" || installs != 1 {
		t.Fatalf("same-version pin=%+v installs=%d", got, installs)
	}
	if out := runAgents(t, e, 0, "enable", "codex"); !strings.Contains(out, "pin 1.2.3") {
		t.Fatalf("idempotent output omitted pin: %s", out)
	}
	runAgents(t, e, 0, "enable", "codex", "--version", "2.0.0", "--force")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "2.0.0" || got.InstalledVersion != "2.0.0" || installs != 2 {
		t.Fatalf("changed-version pin=%+v installs=%d", got, installs)
	}
	version = "3.0.0"
	runAgents(t, e, 0, "update", "codex")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "2.0.0" || got.InstalledVersion != "2.0.0" || installs != 3 {
		t.Fatalf("pinned update=%+v installs=%d", got, installs)
	}
	runAgents(t, e, 0, "update", "codex", "--unpin", "--force")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "" || got.InstalledVersion != "3.0.0" || installs != 4 {
		t.Fatalf("unpin=%+v installs=%d", got, installs)
	}
	version = "4.0.0"
	runAgents(t, e, 0, "update", "codex", "--unpin")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "" || got.InstalledVersion != "4.0.0" || installs != 5 {
		t.Fatalf("already unpinned update=%+v installs=%d", got, installs)
	}
	version = "5.0.0"
	runAgents(t, e, 0, "update", "codex")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "" || got.InstalledVersion != "5.0.0" || installs != 6 {
		t.Fatalf("unpinned update=%+v installs=%d", got, installs)
	}
	runAgents(t, e, 0, "enable", "codex", "--version", "5.0.0")
	if out := runAgents(t, e, 0, "disable", "codex"); !strings.Contains(out, "removed pin 5.0.0") {
		t.Fatalf("disable output=%s", out)
	}
	version = "6.0.0"
	runAgents(t, e, 0, "enable", "codex")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "" || got.InstalledVersion != "6.0.0" || installs != 7 {
		t.Fatalf("reenable=%+v installs=%d", got, installs)
	}
	if out := runAgents(t, e, 1, "update", "claude"); !strings.Contains(out, "agents enable NAME claude") {
		t.Fatalf("missing update guidance=%s", out)
	}
}

// TestFailedAgentInstallPreservesSelectionAndCommand proves npm failures never enable a missing agent or remove a prior version or pin at RunWithEffects and the real home filesystem seam.
func TestFailedAgentInstallPreservesSelectionAndCommand(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	failing := e
	failing.Start = func(manager.Process) error { return fmt.Errorf("npm unavailable") }
	runAgents(t, failing, 1, "enable", "claude")
	if out := runAgents(t, e, 0, "list"); out != "[]\n" {
		t.Fatalf("failed install enabled agent: %s", out)
	}
	runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
	command := filepath.Join(home, ".local", "bin", "codex")
	before, err := os.Readlink(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"enable", "codex", "--version", "2.0.0"}, {"update", "codex"}, {"update", "codex", "--unpin"}} {
		runAgents(t, failing, 1, args...)
		if got := readAgentStatus(t, e, "codex"); got.InstalledVersion != "1.0.0" || got.Pin != "1.0.0" {
			t.Fatalf("failed %v changed status=%+v", args, got)
		}
		if after, err := os.Readlink(command); err != nil || after != before {
			t.Fatalf("failed %v changed command=%s err=%v", args, after, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, ".local", "share", "sandboxed-agents"))
	if err != nil {
		t.Fatal(err)
	}
	prefixes := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "installation-") {
			prefixes++
		}
	}
	if prefixes != 1 {
		t.Fatalf("failed installation staging leftovers=%v", entries)
	}
}

// TestAgentRequestsValidateBeforeHomeOrProcesses proves unknown agents and unsafe versions are refused before home access, npm, and worker re-exec at RunWithEffects.
func TestAgentRequestsValidateBeforeHomeOrProcesses(t *testing.T) {
	for _, identity := range []manager.Identity{{UID: 0, GID: 0}, {UID: 1000, GID: 1000}} {
		e := noProcesses(t, identity)
		e.HomeDirectory = func() (string, error) { t.Error("validation read home"); return "", nil }
		for _, operation := range []string{"enable", "disable", "update", "status"} {
			if output := runAgents(t, e, 2, operation, "unknown"); !strings.Contains(output, "valid names: claude, codex, copilot, opencode") {
				t.Fatalf("unknown names=%s", output)
			}
		}
		for _, pin := range []string{"", "latest", "^1.2.3", "~1.2.3", "1", "1.2", "-x", "1.2.3;id", "file:/tmp/package", "1.2.3\n"} {
			runAgents(t, e, 2, "enable", "codex", "--version", pin)
		}
		for _, args := range [][]string{{}, {"list", "extra"}, {"enable"}, {"enable", "codex", "extra"}, {"status", "codex", "--force"}, {"update", "codex", "--version", "1.2.3"}, {"disable", "codex", "--unpin"}, {"run", "codex"}} {
			runAgents(t, e, 2, args...)
		}
	}
}

// TestAgentAdministrationReexecutesBeforeHomeAccess proves root lifecycle and list calls re-exec a 1000:1000 worker before accessing home through the manager OS-effect seam.
func TestAgentAdministrationReexecutesBeforeHomeAccess(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	worker := agentEffects(t, home, &version, &installs)
	root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	root.HomeDirectory = func() (string, error) { t.Fatal("root accessed home"); return "", nil }
	root.UIDs = func() (int, int, int) { t.Fatal("root read agent UID"); return 0, 0, 0 }
	root.GIDs = func() (int, int, int) { t.Fatal("root read agent GID"); return 0, 0, 0 }
	root.Groups = func() ([]int, error) { t.Fatal("root read agent groups"); return nil, nil }
	workers := 0
	root.Reexec = func(p manager.Process) error {
		workers++
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || len(p.Args) < 3 || p.Args[0] != "__worker" || p.Args[1] != "agents" {
			t.Fatalf("worker request=%+v", p)
		}
		status := manager.RunWithEffects(p.Args, p.Stdout, p.Stderr, worker)
		if status != 0 {
			return childExit(status)
		}
		return nil
	}
	for _, args := range [][]string{{"enable", "codex"}, {"status", "codex"}, {"update", "codex"}, {"list"}, {"disable", "codex"}} {
		runAgents(t, root, 0, args...)
	}
	if workers != 5 || installs != 2 {
		t.Fatalf("workers=%d installs=%d", workers, installs)
	}
}

// TestAgentWorkerRefusesUnsafeIdentityBeforeHome proves every saved, real, and effective ID and group is verified before agent administration reads home at RunWithEffects.
func TestAgentWorkerRefusesUnsafeIdentityBeforeHome(t *testing.T) {
	for _, change := range []func(*manager.Effects){
		func(e *manager.Effects) { e.UIDs = nil },
		func(e *manager.Effects) { e.GIDs = nil },
		func(e *manager.Effects) { e.Groups = nil },
		func(e *manager.Effects) { e.Groups = func() ([]int, error) { return []int{0}, nil } },
		func(e *manager.Effects) {
			e.Groups = func() ([]int, error) { return nil, fmt.Errorf("group lookup failed") }
		},
		func(e *manager.Effects) { e.UIDs = func() (int, int, int) { return 0, 1000, 1000 } },
		func(e *manager.Effects) { e.UIDs = func() (int, int, int) { return 1000, 0, 1000 } },
		func(e *manager.Effects) { e.UIDs = func() (int, int, int) { return 1000, 1000, 0 } },
		func(e *manager.Effects) { e.GIDs = func() (int, int, int) { return 0, 1000, 1000 } },
		func(e *manager.Effects) { e.GIDs = func() (int, int, int) { return 1000, 0, 1000 } },
		func(e *manager.Effects) { e.GIDs = func() (int, int, int) { return 1000, 1000, 0 } },
	} {
		e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
		change(&e)
		e.HomeDirectory = func() (string, error) { t.Fatal("unsafe worker accessed home"); return "", nil }
		for _, args := range [][]string{{"list"}, {"enable", "codex"}, {"disable", "codex"}, {"update", "codex"}, {"status", "codex"}} {
			var out bytes.Buffer
			status := manager.RunWithEffects(append([]string{"__worker", "agents"}, args...), &out, &out, e)
			if status != 1 {
				t.Fatalf("unsafe worker status=%d output=%s", status, &out)
			}
		}
	}
}

// TestCatalogEntryAloneDeliversNpmAgent proves adding shared npm catalog data makes the full manager lifecycle available without agent-specific code at RunWithEffects.
func TestCatalogEntryAloneDeliversNpmAgent(t *testing.T) {
	catalog.Default.Agents["new-agent"] = catalog.Agent{Delivered: true, Install: catalog.Install{Kind: catalog.NPM, Package: "@example/new-agent"}, Command: "new-command"}
	t.Cleanup(func() { delete(catalog.Default.Agents, "new-agent") })
	home, version, installs := t.TempDir(), "1.0.0-beta.2+build.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "new-agent", "--version", version)
	if got := readAgentStatus(t, e, "new-agent"); !got.Enabled || got.InstalledVersion != version || got.Pin != version {
		t.Fatalf("new agent status=%+v", got)
	}
	runAgents(t, e, 0, "update", "new-agent")
	runAgents(t, e, 0, "disable", "new-agent")
	if got := readAgentStatus(t, e, "new-agent"); got.Enabled {
		t.Fatalf("new agent retained=%+v", got)
	}
	if installs != 2 {
		t.Fatalf("installs=%d", installs)
	}
}

// TestConcurrentEnablesPreserveSelection proves a second enable and list wait for an in-flight install and retain both selections through RunWithEffects and a real manager lock in a temporary home.
func TestConcurrentEnablesPreserveSelection(t *testing.T) {
	home, version := t.TempDir(), "1.0.0"
	firstInstalls, secondInstalls := 0, 0
	first := agentEffects(t, home, &version, &firstInstalls)
	second := agentEffects(t, home, &version, &secondInstalls)
	insideFirst, releaseFirst, insideSecondHome := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	}()
	firstStart := first.Start
	first.Start = func(p manager.Process) error { close(insideFirst); <-releaseFirst; return firstStart(p) }
	second.HomeDirectory = func() (string, error) { close(insideSecondHome); return home, nil }
	secondStart := second.Start
	var secondStarted atomic.Bool
	second.Start = func(p manager.Process) error { secondStarted.Store(true); return secondStart(p) }
	type result struct {
		status int
		output string
	}
	invoke := func(e manager.Effects, args []string, done chan<- result) {
		var out bytes.Buffer
		status := manager.RunWithEffects(args, &out, &out, e)
		done <- result{status, out.String()}
	}
	firstDone, secondDone, listDone := make(chan result, 1), make(chan result, 1), make(chan result, 1)
	go invoke(first, []string{"agents", "enable", "codex"}, firstDone)
	<-insideFirst
	go invoke(second, []string{"agents", "enable", "claude"}, secondDone)
	<-insideSecondHome
	list := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	insideListHome := make(chan struct{})
	list.HomeDirectory = func() (string, error) { close(insideListHome); return home, nil }
	go invoke(list, []string{"agents", "list"}, listDone)
	<-insideListHome
	select {
	case got := <-listDone:
		t.Fatalf("list observed incomplete installation: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	if secondStarted.Load() {
		t.Fatal("second enable entered npm while first install held manager lock")
	}
	close(releaseFirst)
	for _, done := range []<-chan result{firstDone, secondDone, listDone} {
		got := <-done
		if got.status != 0 {
			t.Fatalf("concurrent command failed: %+v", got)
		}
	}
	final := agentEffects(t, home, &version, &firstInstalls)
	if output := runAgents(t, final, 0, "list"); output != "[\"claude\",\"codex\"]\n" {
		t.Fatalf("concurrent enables lost selection: %s", output)
	}
}

// TestAgentStatusUsesOnlyUsableWorkerProbe proves an enabled agent's optional probe starts unprivileged and only a successful boolean JSON field reports sign-in at RunWithEffects.
func TestAgentStatusUsesOnlyUsableWorkerProbe(t *testing.T) {
	original := catalog.Default.Agents["codex"]
	definition := original
	definition.StatusProbe = &catalog.Probe{Args: []string{"auth", "status", "--json"}, BooleanField: "loggedIn"}
	catalog.Default.Agents["codex"] = definition
	t.Cleanup(func() { catalog.Default.Agents["codex"] = original })
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	for _, tc := range []struct {
		output  string
		failure error
		want    string
	}{
		{`{"loggedIn":true}`, nil, "yes"},
		{`{"loggedIn":false}`, nil, "no"},
		{`{"loggedIn":"true"}`, nil, "unknown"},
		{`{"other":true}`, nil, "unknown"},
		{`not json`, nil, "unknown"},
		{`{"loggedIn":true}`, fmt.Errorf("probe failed"), "unknown"},
	} {
		calls := 0
		e.Start = func(p manager.Process) error {
			calls++
			if p.Program != filepath.Join(home, ".local", "bin", "codex") || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || strings.Join(p.Args, " ") != "auth status --json" {
				t.Fatalf("probe process=%+v", p)
			}
			fmt.Fprint(p.Stdout, tc.output)
			fmt.Fprint(p.Stderr, "ignored authentication diagnostics")
			return tc.failure
		}
		root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
		root.Reexec = func(p manager.Process) error {
			status := manager.RunWithEffects(p.Args, p.Stdout, p.Stderr, e)
			if status != 0 {
				return childExit(status)
			}
			return nil
		}
		if got := readAgentStatus(t, root, "codex"); got.SignedIn != tc.want || calls != 1 {
			t.Fatalf("probe output=%q status=%+v calls=%d", tc.output, got, calls)
		}
	}
	e.Start = func(manager.Process) error { t.Error("disabled probe started"); return nil }
	runAgents(t, e, 0, "disable", "codex")
	if got := readAgentStatus(t, e, "codex"); got.SignedIn != "unknown" {
		t.Fatalf("disabled sign-in=%+v", got)
	}
}

// TestAgentInstallationFailureReturnsManagerFailure proves npm exit status is reported as manager failure rather than passed through at RunWithEffects, preserving selection.
func TestAgentInstallationFailureReturnsManagerFailure(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	e.Start = func(manager.Process) error { return childExit(23) }
	runAgents(t, e, 1, "enable", "codex")
	if got := readAgentStatus(t, e, "codex"); got.Enabled {
		t.Fatalf("failed install enabled=%+v", got)
	}
}
