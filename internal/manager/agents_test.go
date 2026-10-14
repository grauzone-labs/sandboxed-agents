package manager_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// agentEffects fakes npm at the process seam while writing installation artifacts into the test home.
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
		if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":%q}`, packageName, installed)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(filepath.Join(prefix, "bin", command), []byte("managed command"), 0700)
	}
	return e
}

// runAgents exercises the public manager boundary and checks its exit status before returning output.
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
			for _, args := range [][]string{{"enable", name}, {"enable", name, "--force"}} {
				if out := runAgents(t, e, 0, args...); !strings.Contains(out, "version 2.3.4 (pin none)") {
					t.Fatalf("enable omitted installed version or absent pin: %s", out)
				}
			}
			if status := readAgentStatus(t, e, name); !status.Enabled || status.InstalledVersion != "2.3.4" || status.Pin != "" {
				t.Fatalf("enabled status=%+v", status)
			}
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

// readAgentStatus decodes the status returned by the public manager boundary.
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
	if out := runAgents(t, e, 1, "update", "claude"); !strings.Contains(out, "claude not enabled") || strings.Contains(out, "NAME") {
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
		for _, pin := range []string{"", "latest", "^1.2.3", "~1.2.3", "1", "1.2", "-x", "1.2.3;id", "1.2.3+build.4", "1.2.3-rc.1+build.4", "file:/tmp/package", "1.2.3\n"} {
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
	home, version, installs := t.TempDir(), "1.0.0-beta.2", 0
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

// TestConcurrentEnablesPreserveSelection proves competing enables refuse promptly, read-only queries observe atomic selection during npm, and an explicit retry preserves both agents at RunWithEffects with a real manager lock.
func TestConcurrentEnablesPreserveSelection(t *testing.T) {
	home, version := t.TempDir(), "1.0.0"
	firstInstalls, secondInstalls := 0, 0
	first := agentEffects(t, home, &version, &firstInstalls)
	second := agentEffects(t, home, &version, &secondInstalls)
	insideFirst, releaseFirst := make(chan struct{}), make(chan struct{})
	firstStart := first.Start
	first.Start = func(p manager.Process) error { close(insideFirst); <-releaseFirst; return firstStart(p) }
	type result struct {
		status int
		output string
	}
	invoke := func(e manager.Effects, args []string, done chan<- result) {
		var out bytes.Buffer
		status := manager.RunWithEffects(args, &out, &out, e)
		done <- result{status, out.String()}
	}
	firstDone, secondDone := make(chan result, 1), make(chan result, 1)
	firstFinished, secondFinished := false, false
	go invoke(first, []string{"agents", "enable", "codex"}, firstDone)
	<-insideFirst
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		if !firstFinished {
			<-firstDone
		}
		if !secondFinished {
			<-secondDone
		}
	}()
	go invoke(second, []string{"agents", "enable", "claude"}, secondDone)
	select {
	case got := <-secondDone:
		secondFinished = true
		if got.status != 1 || !strings.Contains(got.output, "another agent change is in progress; retry") {
			t.Fatalf("competing enable=%+v", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("competing enable waited for npm instead of refusing")
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agents", "list"}, "[]\n"},
		{[]string{"agents", "status", "codex"}, ""},
	} {
		done := make(chan result, 1)
		go invoke(second, tc.args, done)
		select {
		case got := <-done:
			if got.status != 0 {
				t.Fatalf("read during npm=%+v", got)
			}
			if tc.want != "" && got.output != tc.want {
				t.Fatalf("atomic selection=%q want=%q", got.output, tc.want)
			}
			if tc.want == "" {
				var status manager.AgentStatus
				if err := json.Unmarshal([]byte(got.output), &status); err != nil || status.Enabled {
					t.Fatalf("in-flight install published status=%+v error=%v", status, err)
				}
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("read-only %v waited for npm", tc.args)
		}
	}
	close(releaseFirst)
	firstFinished = true
	if got := <-firstDone; got.status != 0 {
		t.Fatalf("first enable=%+v", got)
	}
	runAgents(t, second, 0, "enable", "claude")
	if output := runAgents(t, second, 0, "list"); output != "[\"claude\",\"codex\"]\n" {
		t.Fatalf("retry lost selection: %s", output)
	}
	if firstInstalls != 1 || secondInstalls != 1 {
		t.Fatalf("installation counts first=%d second=%d", firstInstalls, secondInstalls)
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

	command := filepath.Join(home, ".local", "bin", "codex")
	previous, err := os.Readlink(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("unmanaged command"), 0700); err != nil {
		t.Fatal(err)
	}
	e.Start = func(manager.Process) error { t.Error("unmanaged probe started"); return nil }
	if got := readAgentStatus(t, e, "codex"); got.SignedIn != "unknown" {
		t.Fatalf("unmanaged sign-in=%+v", got)
	}
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(previous, command); err != nil {
		t.Fatal(err)
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

// interruptedAgentLink creates the on-disk command publication left before its selection was committed.
func interruptedAgentLink(t *testing.T, home, name, version string) string {
	t.Helper()
	definition := catalog.Default.Agents[name]
	prefix := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-interrupted")
	pkg := filepath.Join(prefix, "lib", "node_modules", definition.Install.Package)
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":%q}`, definition.Install.Package, version)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "cli.js"), []byte("orphan managed command"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(bin, definition.Command)
	relative, err := filepath.Rel(bin, filepath.Join(pkg, "cli.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(home, ".local", "bin", definition.Command)
	if err := os.Remove(command); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, command); err != nil {
		t.Fatal(err)
	}
	return target
}

// TestForcedEnableRepairsInterruptedPublication proves normal enable refuses a mismatched managed link while explicit force reinstalls the selected pin through RunWithEffects and a real temporary home.
func TestForcedEnableRepairsInterruptedPublication(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
	orphan := interruptedAgentLink(t, home, "codex", "2.0.0")
	if out := runAgents(t, e, 1, "enable", "codex", "--version", "1.0.0"); !strings.Contains(out, "--force") {
		t.Fatalf("missing repair hint: %s", out)
	}
	if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" {
		t.Fatalf("refusal changed selection=%+v", got)
	}
	version = "3.0.0"
	runAgents(t, e, 0, "enable", "codex", "--force")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" || installs != 2 {
		t.Fatalf("forced repair lost pin/status=%+v installs=%d", got, installs)
	}
	target, err := os.Readlink(filepath.Join(home, ".local", "bin", "codex"))
	if err != nil || target == orphan {
		t.Fatalf("forced repair left orphan link=%s error=%v", target, err)
	}
	runAgents(t, e, 0, "disable", "codex")
}

// TestForcedUpdateAndDisableRepairInterruptedPublication proves force repairs and cleans a mismatched npm publication while ordinary mutations refuse and unrelated selections survive at RunWithEffects.
func TestForcedUpdateAndDisableRepairInterruptedPublication(t *testing.T) {
	for _, operation := range []string{"update", "disable"} {
		t.Run(operation, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			preserved := []string{".git-credentials", ".npm/cache", ".config/codex/auth.json"}
			for _, name := range preserved {
				path := filepath.Join(home, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("preserve "+name), 0600); err != nil {
					t.Fatal(err)
				}
			}

			runAgents(t, e, 0, "enable", "claude")
			runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
			interruptedAgentLink(t, home, "codex", "2.0.0")
			before := installs
			runAgents(t, e, 1, operation, "codex")
			if installs != before {
				t.Fatalf("ordinary mutation started npm: %d != %d", installs, before)
			}
			runAgents(t, e, 0, operation, "codex", "--force")
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-interrupted")); !os.IsNotExist(err) {
				t.Fatalf("recovered orphan installation survived: %v", err)
			}
			got := readAgentStatus(t, e, "codex")
			if operation == "update" && (!got.Enabled || got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0") {
				t.Fatalf("forced update=%+v", got)
			}
			if operation == "disable" {
				if got.Enabled {
					t.Fatalf("forced disable retained=%+v", got)
				}
				if _, err := os.Lstat(filepath.Join(home, ".local", "bin", "codex")); !os.IsNotExist(err) {
					t.Fatalf("force retained command: %v", err)
				}
			}
			for _, name := range preserved {
				if data, err := os.ReadFile(filepath.Join(home, name)); err != nil || string(data) != "preserve "+name {
					t.Fatalf("recovery changed %s=%q error=%v", name, data, err)
				}
			}
			if got := readAgentStatus(t, e, "claude"); !got.Enabled || got.InstalledVersion != "1.0.0" {
				t.Fatalf("other agent changed=%+v", got)
			}
		})
	}
}

// TestForcedEnableRepairsMissingCommand proves a missing enabled command cannot silently succeed or repin and force preserves its pin at RunWithEffects with a real home.
func TestForcedEnableRepairsMissingCommand(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
	if err := os.Remove(filepath.Join(home, ".local", "bin", "codex")); err != nil {
		t.Fatal(err)
	}
	if output := runAgents(t, e, 1, "enable", "codex", "--version", "2.0.0"); !strings.Contains(output, "--force") {
		t.Fatalf("missing repair hint: %s", output)
	}
	if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || installs != 1 {
		t.Fatalf("refusal repinned=%+v installs=%d", got, installs)
	}
	runAgents(t, e, 0, "enable", "codex", "--force")
	if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" || installs != 2 {
		t.Fatalf("missing command repair=%+v installs=%d", got, installs)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "codex")); err != nil {
		t.Fatalf("missing command unrepaired: %v", err)
	}
}

// TestForcedMutationsPreserveUnmanagedCommands proves force never replaces or removes a regular command or changes its selected pin at RunWithEffects and the temporary-home filesystem seam.
func TestForcedMutationsPreserveUnmanagedCommands(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
	command := filepath.Join(home, ".local", "bin", "codex")
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("unrelated command"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"enable", "codex", "--force"}, {"enable", "codex", "--version", "2.0.0", "--force"}, {"update", "codex", "--force"}, {"disable", "codex", "--force"}} {
		output := runAgents(t, e, 1, args...)
		if strings.Contains(output, "retry with --force") {
			t.Fatalf("unsafe command implied bypass: %s", output)
		}
		if got := readAgentStatus(t, e, "codex"); !got.Enabled || got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" || installs != 1 {
			t.Fatalf("refusal changed selection=%+v installs=%d", got, installs)
		}
		if data, err := os.ReadFile(command); err != nil || string(data) != "unrelated command" {
			t.Fatalf("unmanaged command changed=%q error=%v", data, err)
		}
	}
}

// TestForcedRecoveryRejectsEscapingOrWrongAgentLinks proves force refuses foreign package identity, traversal, and symlink escapes before npm while preserving selection and the link at RunWithEffects.
func TestForcedRecoveryRejectsEscapingOrWrongAgentLinks(t *testing.T) {
	for _, kind := range []string{"outside", "relative-traversal", "wrong-agent", "prefix-symlink", "bin-symlink", "command-symlink", "metadata-symlink", "invalid-version", "selected-prefix-symlink", "selected-bin-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
			command := filepath.Join(home, ".local", "bin", "codex")
			target, err := os.Readlink(command)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(kind, "selected-") {
				target = interruptedAgentLink(t, home, "codex", "2.0.0")
			}
			prefix := filepath.Dir(filepath.Dir(target))
			metadata := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex", "package.json")
			outside := t.TempDir()
			external := filepath.Join(outside, "codex")
			if err := os.WriteFile(external, []byte("unrelated external command"), 0700); err != nil {
				t.Fatal(err)
			}
			replace := func(path, link string) {
				t.Helper()
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(link, path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "outside":
				replace(command, external)
			case "relative-traversal":
				replace(command, "../../share/sandboxed-agents/installation-interrupted/bin/codex")
			case "wrong-agent":
				if err := os.WriteFile(metadata, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.0.0"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "prefix-symlink", "selected-prefix-symlink":
				moved := filepath.Join(outside, "installation-moved")
				if err := os.Rename(prefix, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, prefix); err != nil {
					t.Fatal(err)
				}
			case "bin-symlink", "selected-bin-symlink":
				replace(filepath.Join(prefix, "bin"), outside)
			case "command-symlink":
				replace(target, external)
			case "metadata-symlink":
				path := filepath.Join(outside, "package.json")
				if err := os.WriteFile(path, []byte(`{"name":"@openai/codex","version":"2.0.0"}`), 0600); err != nil {
					t.Fatal(err)
				}
				replace(metadata, path)
			case "invalid-version":
				if err := os.WriteFile(metadata, []byte(`{"name":"@openai/codex","version":"latest"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			previous, err := os.Readlink(command)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"enable", "update", "disable"} {
				runAgents(t, e, 1, operation, "codex", "--force")
				if installs != 1 {
					t.Fatalf("unsafe recovery ran npm: %d", installs)
				}
				if got := readAgentStatus(t, e, "codex"); !got.Enabled || got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" {
					t.Fatalf("unsafe recovery changed selection=%+v", got)
				}
				if after, err := os.Readlink(command); err != nil || after != previous {
					t.Fatalf("unsafe recovery changed link=%s error=%v", after, err)
				}
				if data, err := os.ReadFile(external); err != nil || string(data) != "unrelated external command" {
					t.Fatalf("external command changed=%q error=%v", data, err)
				}
			}
		})
	}
}

// TestForcedRecoveryHandlesInitialPublication proves force enables or disables and cleans a validated orphan without a selection while leaving unrelated prefixes alone at RunWithEffects.
func TestForcedRecoveryHandlesInitialPublication(t *testing.T) {
	for _, operation := range []string{"enable", "disable"} {
		t.Run(operation, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex")
			runAgents(t, e, 0, "disable", "codex")
			interruptedAgentLink(t, home, "codex", "2.0.0")
			unrelated := filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-unrelated")
			if err := os.Mkdir(unrelated, 0700); err != nil {
				t.Fatal(err)
			}
			if operation == "enable" {
				runAgents(t, e, 1, "enable", "codex")
			} else {
				runAgents(t, e, 0, "disable", "codex")
			}
			version = "3.0.0"
			runAgents(t, e, 0, operation, "codex", "--force")
			if _, err := os.Stat(filepath.Join(home, ".local", "share", "sandboxed-agents", "installation-interrupted")); !os.IsNotExist(err) {
				t.Fatalf("recovered orphan installation survived: %v", err)
			}
			if _, err := os.Stat(unrelated); err != nil {
				t.Fatalf("unrelated prefix changed: %v", err)
			}
			got := readAgentStatus(t, e, "codex")
			if operation == "enable" && (!got.Enabled || got.InstalledVersion != "3.0.0" || got.Pin != "" || installs != 2) {
				t.Fatalf("initial force enable=%+v installs=%d", got, installs)
			}
			if operation == "disable" {
				if got.Enabled || installs != 1 {
					t.Fatalf("initial force disable=%+v installs=%d", got, installs)
				}
				if _, err := os.Lstat(filepath.Join(home, ".local", "bin", "codex")); !os.IsNotExist(err) {
					t.Fatalf("orphan link survived disable: %v", err)
				}
			}
		})
	}
}

// TestFailedForcedRecoveryPreservesPublication proves a failed repair keeps the old selection pin and interrupted command link for a later retry at RunWithEffects.
func TestFailedForcedRecoveryPreservesPublication(t *testing.T) {
	home, version, installs := t.TempDir(), "1.0.0", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
	orphan := interruptedAgentLink(t, home, "codex", "2.0.0")
	failed := e
	failed.Start = func(manager.Process) error { return fmt.Errorf("npm unavailable") }
	for _, operation := range []string{"enable", "update"} {
		runAgents(t, failed, 1, operation, "codex", "--force")
		if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" {
			t.Fatalf("failed force changed selection=%+v", got)
		}
		if target, err := os.Readlink(filepath.Join(home, ".local", "bin", "codex")); err != nil || target != orphan {
			t.Fatalf("failed force changed publication=%s error=%v", target, err)
		}
	}
}

// TestOrdinaryMutationsHandleMissingCommand proves update restores a pinned command and disable removes its selection and names the pin without force while preserving other home data at RunWithEffects.
func TestOrdinaryMutationsHandleMissingCommand(t *testing.T) {
	for _, operation := range []string{"update", "disable"} {
		t.Run(operation, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			preserved := []string{".git-credentials", ".npm/cache"}
			for _, name := range preserved {
				path := filepath.Join(home, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("preserve "+name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			runAgents(t, e, 0, "enable", "claude")
			runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
			command := filepath.Join(home, ".local", "bin", "codex")
			if err := os.Remove(command); err != nil {
				t.Fatal(err)
			}
			version = "2.0.0"
			output := runAgents(t, e, 0, operation, "codex")
			got := readAgentStatus(t, e, "codex")
			if operation == "update" {
				if !got.Enabled || got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" || installs != 3 {
					t.Fatalf("ordinary update=%+v installs=%d", got, installs)
				}
				if _, err := os.Stat(command); err != nil {
					t.Fatalf("ordinary update did not restore command: %v", err)
				}
			} else {
				if got.Enabled || got.Pin != "" || got.InstalledVersion != "" || installs != 2 || !strings.Contains(output, "removed pin 1.0.0") {
					t.Fatalf("ordinary disable=%+v installs=%d output=%s", got, installs, output)
				}
				if _, err := os.Lstat(command); !os.IsNotExist(err) {
					t.Fatalf("ordinary disable recreated command: %v", err)
				}
				if list := runAgents(t, e, 0, "list"); list != "[\"claude\"]\n" {
					t.Fatalf("ordinary disable retained selection: %s", list)
				}
			}
			if other := readAgentStatus(t, e, "claude"); !other.Enabled || other.InstalledVersion != "1.0.0" {
				t.Fatalf("other selection changed=%+v", other)
			}
			for _, name := range preserved {
				if data, err := os.ReadFile(filepath.Join(home, name)); err != nil || string(data) != "preserve "+name {
					t.Fatalf("ordinary mutation changed %s=%q error=%v", name, data, err)
				}
			}
		})
	}
}

// TestRecoveredCleanupPreservesReferencedPrefixes proves forced changes retain a recovered prefix still referenced by another selection at RunWithEffects and the real home-state seam.
func TestRecoveredCleanupPreservesReferencedPrefixes(t *testing.T) {
	for _, operation := range []string{"enable", "update", "disable"} {
		t.Run(operation, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "claude")
			runAgents(t, e, 0, "enable", "codex")
			orphan := interruptedAgentLink(t, home, "codex", "2.0.0")
			selectionPath := filepath.Join(home, ".local", "share", "sandboxed-agents", "selection.json")
			data, err := os.ReadFile(selectionPath)
			if err != nil {
				t.Fatal(err)
			}
			var selection map[string]map[string]string
			if err := json.Unmarshal(data, &selection); err != nil {
				t.Fatal(err)
			}
			selection["claude"]["prefix"] = "installation-interrupted"
			data, err = json.Marshal(selection)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(selectionPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			runAgents(t, e, 0, operation, "codex", "--force")
			if _, err := os.Stat(orphan); err != nil {
				t.Fatalf("referenced installation removed: %v", err)
			}
			data, err = os.ReadFile(selectionPath)
			if err != nil {
				t.Fatal(err)
			}
			selection = nil
			if err := json.Unmarshal(data, &selection); err != nil {
				t.Fatal(err)
			}
			if selection["claude"]["prefix"] != "installation-interrupted" {
				t.Fatalf("other selection changed: %v", selection["claude"])
			}
		})
	}
}

// TestFailedSelectionCommitPreservesRecoveredPrefix proves failed forced publication restores the prior link without deleting its recovered installation at RunWithEffects and the temporary-home filesystem seam.
func TestFailedSelectionCommitPreservesRecoveredPrefix(t *testing.T) {
	for _, operation := range []string{"enable", "update"} {
		t.Run(operation, func(t *testing.T) {
			home, version, installs := t.TempDir(), "1.0.0", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "codex", "--version", "1.0.0")
			orphan := interruptedAgentLink(t, home, "codex", "2.0.0")
			selectionPath := filepath.Join(home, ".local", "share", "sandboxed-agents", "selection.json")
			start := e.Start
			e.Start = func(p manager.Process) error {
				if err := start(p); err != nil {
					return err
				}
				if err := os.Rename(selectionPath, selectionPath+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(selectionPath, 0700); err != nil {
					t.Fatal(err)
				}
				return nil
			}
			runAgents(t, e, 1, operation, "codex", "--force")
			if _, err := os.Stat(orphan); err != nil {
				t.Fatalf("uncommitted recovery removed prior installation: %v", err)
			}
			command := filepath.Join(home, ".local", "bin", "codex")
			if target, err := os.Readlink(command); err != nil || target != orphan {
				t.Fatalf("prior link not restored: target=%s error=%v", target, err)
			}
			if err := os.Remove(selectionPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(selectionPath+".saved", selectionPath); err != nil {
				t.Fatal(err)
			}
			if got := readAgentStatus(t, e, "codex"); got.Pin != "1.0.0" || got.InstalledVersion != "1.0.0" {
				t.Fatalf("failed publication changed selection: %+v", got)
			}
		})
	}
}
