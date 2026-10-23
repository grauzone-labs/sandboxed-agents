package manager_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// homeSnapshot records every file outside the Tool directory with its content, so a test can prove that connect actions touch no agent credential path.
func homeSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	tool := toolDirectory(home)
	err := filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(path, tool) || info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// credentialPath is the account credential of the fake upstream in the test home.
func credentialPath(home string) string {
	return filepath.Join(toolDirectory(home), "home", "userdata", "secrets", "cloud-cli-oauth-token.bin")
}

// enabledTool enables T3 Code in a fresh test home with a running fake service and agent credential files to protect.
func enabledTool(t *testing.T) (string, *toolRun, manager.Effects, *fakeServices) {
	t.Helper()
	home, run := t.TempDir(), &toolRun{}
	for _, file := range []string{".git-credentials", ".config/gh/hosts.yml", ".azure/msal_token_cache.json", ".claude/credentials.json", ".codex/auth.json"} {
		path := filepath.Join(home, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("preserve "+file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	runTools(t, e, 0, "enable", "t3code")
	return home, run, e, services
}

// TestConnectLoginRunsHeadlessAsAgentUnderTheConnectLock proves login runs the upstream headless login as agent with the Tool's home as its only extra environment and the caller's terminal input, shows upstream's URL and code, stores the credential only under the Tool's home and changes no other file, watches for Ctrl+C while the child runs, and refuses while another login holds the connect lock, at RunWithEffects with a real lock.
func TestConnectLoginRunsHeadlessAsAgentUnderTheConnectLock(t *testing.T) {
	home, run, e, _ := enabledTool(t)
	before := homeSnapshot(t, home)
	e.Stdin = strings.NewReader("terminal input")
	out := runTools(t, e, 0, "connect", "t3code", "login")
	if !strings.Contains(out, "https://accounts.t3.codes/device?user_code=ABCD-EFGH") || !strings.Contains(out, "expires in 10 min") {
		t.Fatalf("login output=%s", out)
	}
	if len(run.connectCalls) != 1 || !slices.Equal(run.connectCalls[0], []string{"connect", "login", "--headless"}) || run.connectInputs[0] != "terminal input" || run.interruptWatches != 1 {
		t.Fatalf("connect calls=%v inputs=%q interrupt watches=%d", run.connectCalls, run.connectInputs, run.interruptWatches)
	}
	// Reads of agent credential paths are excluded structurally: upstream keeps its state under the Tool's
	// home, which is the only variable the process receives beyond the agent environment policy.
	if env := run.connectEnvironments[0]; len(env) != 1 || !strings.HasPrefix(env[0], "T3CODE_HOME=") {
		t.Fatalf("connect process environment=%q", env)
	}
	if _, err := os.Stat(credentialPath(home)); err != nil {
		t.Fatalf("credential not stored under the Tool's home: %v", err)
	}
	if after := homeSnapshot(t, home); !equalSnapshots(before, after) {
		t.Fatalf("login changed files outside the Tool directory: before=%v after=%v", before, after)
	}
	release, held, err := lockConnect(home)
	if err != nil || !held {
		t.Fatalf("cannot take the connect lock: %v", err)
	}
	defer release()
	if out := runTools(t, e, 1, "connect", "t3code", "login"); !strings.Contains(out, "another tool change is in progress; retry") || len(run.connectCalls) != 1 {
		t.Fatalf("login under a held connect lock: %s calls=%v", out, run.connectCalls)
	}
}

// equalSnapshots compares two home snapshots.
func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, content := range a {
		if b[path] != content {
			return false
		}
	}
	return true
}

// lockConnect takes the Tool's connect lock in the test home, as a running login would.
func lockConnect(home string) (func(), bool, error) {
	return lockPath(filepath.Join(toolDirectory(home), "connect.lock"))
}

// TestConnectLoginFailuresAreDistinctAndLeaveNoCredential proves denial, expiry, a request failure, and Ctrl+C each exit non-zero with upstream's own message passed through and the wrapper naming the interruption or the exit status, remove a credential the failed run left behind, restore a credential that existed before, keep every other file, and that a later login starts clean, at RunWithEffects with injected process results.
func TestConnectLoginFailuresAreDistinctAndLeaveNoCredential(t *testing.T) {
	home, run, e, _ := enabledTool(t)
	before := homeSnapshot(t, home)
	run.connectLeavesPartial = true
	failures := []struct {
		name, upstream string
		result         error
		interrupt      bool
	}{
		{"denied", "access_denied: the request was denied", childExit(1), false},
		{"expired", "expired_token: the code expired", childExit(1), false},
		{"request-failed", "request failed: accounts.t3.codes unreachable", childExit(1), false},
		{"interrupted", "cancelled", childExit(130), true},
		{"killed", "", childExit(-1), false},
	}
	for _, failure := range failures {
		run.connectResults = map[string]error{"login": failure.result}
		run.connectFailureText, run.interruptDuring = failure.upstream, ""
		if failure.interrupt {
			run.interruptDuring = "login"
		}
		out := runTools(t, e, 1, "connect", "t3code", "login")
		if _, err := os.Stat(credentialPath(home)); !os.IsNotExist(err) {
			t.Fatalf("%s left a credential behind: %v", failure.name, err)
		}
		if failure.upstream != "" && !strings.Contains(out, failure.upstream) {
			t.Fatalf("%s lost upstream's message: %s", failure.name, out)
		}
		interrupted := failure.interrupt || failure.name == "killed"
		if interrupted != strings.Contains(out, "t3code connect login was interrupted") || !interrupted && !strings.Contains(out, "t3code connect login failed with exit status 1") {
			t.Fatalf("%s not named correctly: %s", failure.name, out)
		}
	}
	run.interruptDuring = ""
	if after := homeSnapshot(t, home); !equalSnapshots(before, after) {
		t.Fatalf("failed logins changed files outside the Tool directory")
	}
	run.connectResults, run.connectLeavesPartial = nil, false
	runTools(t, e, 0, "connect", "t3code", "login")
	stored, err := os.ReadFile(credentialPath(home))
	if err != nil {
		t.Fatalf("later login did not store a credential: %v", err)
	}
	// A failed login after a successful one leaves the earlier credential as it was.
	run.connectResults, run.connectLeavesPartial = map[string]error{"login": childExit(1)}, true
	runTools(t, e, 1, "connect", "t3code", "login")
	if restored, err := os.ReadFile(credentialPath(home)); err != nil || string(restored) != string(stored) {
		t.Fatalf("earlier credential not restored: %q error=%v want %q", restored, err, stored)
	}
}

// TestConnectLinkRestartsTheServiceUnderTheManagerLock proves link runs upstream's headless link, then restarts the service on the installed version so that the link takes effect, reports a restart whose stop or start fails, a link that fails, and an interrupted link without restarting, refuses link, unlink, and logout while another change holds the manager lock, and that unlink and logout run without a restart and keep every file outside the Tool directory, at RunWithEffects with a fake service system.
func TestConnectLinkRestartsTheServiceUnderTheManagerLock(t *testing.T) {
	home, run, e, services := enabledTool(t)
	before := homeSnapshot(t, home)
	supervisors := len(services.detachArgs)
	if out := runTools(t, e, 0, "connect", "t3code", "link"); !strings.Contains(out, "t3code service restarted; the link takes effect") {
		t.Fatalf("link output=%s", out)
	}
	if len(services.detachArgs) != supervisors+1 || services.runningServices() != 1 {
		t.Fatalf("link did not restart exactly one service: supervisors=%d running=%d", len(services.detachArgs), services.runningServices())
	}
	for _, action := range []string{"unlink", "logout"} {
		supervisors = len(services.detachArgs)
		runTools(t, e, 0, "connect", "t3code", action)
		if len(services.detachArgs) != supervisors {
			t.Fatalf("%s restarted the service", action)
		}
	}
	if !reflect.DeepEqual(run.connectCalls, [][]string{{"connect", "link", "--headless"}, {"connect", "unlink"}, {"connect", "logout"}}) {
		t.Fatalf("connect calls=%v", run.connectCalls)
	}
	if after := homeSnapshot(t, home); !equalSnapshots(before, after) {
		t.Fatalf("connect actions changed files outside the Tool directory")
	}
	unlock, locked, err := lockFor(t, home)
	if err != nil || !locked {
		t.Fatal(err)
	}
	calls := len(run.connectCalls)
	for _, action := range []string{"link", "unlink", "logout"} {
		if out := runTools(t, e, 1, "connect", "t3code", action); !strings.Contains(out, "another agent or tool change is in progress; retry") {
			t.Fatalf("%s under the manager lock: %s", action, out)
		}
	}
	unlock()
	if len(run.connectCalls) != calls {
		t.Fatalf("refused actions ran upstream: %v", run.connectCalls[calls:])
	}
	services.mu.Lock()
	services.spawnFailures = 5
	services.mu.Unlock()
	if out := runTools(t, e, 1, "connect", "t3code", "link"); !strings.Contains(out, "link recorded, but the service did not start again") || !strings.Contains(out, "tools status NAME t3code") {
		t.Fatalf("failed restart output=%s", out)
	}
	run.connectResults = map[string]error{"link": childExit(1)}
	if out := runTools(t, e, 1, "connect", "t3code", "link"); !strings.Contains(out, "t3code connect link failed with exit status 1") {
		t.Fatalf("failed link output=%s", out)
	}
	supervisors = len(services.detachArgs)
	run.connectResults = nil
	// The service runs again, so the stop is what fails when the manager cannot signal.
	runTools(t, e, 0, "restore")
	unavailable := e
	unavailable.Signal = nil
	if out := runTools(t, unavailable, 1, "connect", "t3code", "link"); !strings.Contains(out, "link recorded, but the service did not stop for its restart") || !strings.Contains(out, "tools status NAME t3code") {
		t.Fatalf("failed stop output=%s", out)
	}
	supervisors = len(services.detachArgs)
	run.connectResults, run.interruptDuring = map[string]error{"link": childExit(130)}, "link"
	if out := runTools(t, e, 1, "connect", "t3code", "link"); !strings.Contains(out, "t3code connect link was interrupted") || len(services.detachArgs) != supervisors {
		t.Fatalf("interrupted link output=%s restarted=%t", out, len(services.detachArgs) != supervisors)
	}
	run.interruptDuring = ""
}

// TestConnectRequiresAnEnabledToolAndKnownAction proves connect actions fail before any process on a Tool that is not enabled or an unknown action, at RunWithEffects.
func TestConnectRequiresAnEnabledToolAndKnownAction(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, _ := serviceEffects(t, home, toolRegistry{}, run)
	if out := runTools(t, e, 1, "connect", "t3code", "login"); !strings.Contains(out, "t3code not enabled; run tools enable NAME t3code") {
		t.Fatalf("disabled output=%s", out)
	}
	if out := runTools(t, e, 2, "connect", "t3code", "pair"); !strings.Contains(out, `unknown connect action "pair": valid names: link, login, logout, unlink`) {
		t.Fatalf("unknown action output=%s", out)
	}
	for _, args := range [][]string{{"connect", "t3code"}, {"connect", "t3code", "login", "extra"}, {"connect", "unknown", "login"}} {
		runTools(t, e, 2, args...)
	}
	if len(run.connectCalls) != 0 {
		t.Fatalf("connect ran upstream: %v", run.connectCalls)
	}
}

// TestToolsStatusReportsTheConnectState proves status with --connect condenses upstream's connect status JSON into the account, linked, relay client, and reachability fields, marks a relay that other devices cannot reach as not reachable with upstream's report, reports a failing status command or a damaged installation in the status without running the launcher, and that list carries no connect part, at RunWithEffects.
func TestToolsStatusReportsTheConnectState(t *testing.T) {
	home, run, e, _ := enabledTool(t)
	status := readFullToolStatus(t, e)
	if status.Connect == nil || status.Connect.Authenticated || status.Connect.Linked || status.Connect.RelayClient != "missing" || status.Connect.Error != "" || status.Connect.Reachability != "" {
		t.Fatalf("default connect status=%+v", status.Connect)
	}
	run.connectStatus = `{"desired":true,"authenticated":true,"linked":true,"revoked":false,"cloudUserId":"user_123","relayUrl":"https://relay.t3.codes","relayClient":{"status":"available","version":"2026.10.0"},"linkMode":"managed","reachability":{"status":"unreachable","reason":"cannot reach region1.v2.argotunnel.com:7844"}}`
	status = readFullToolStatus(t, e)
	if status.Connect == nil || !status.Connect.Authenticated || !status.Connect.Linked || status.Connect.Account != "user_123" || status.Connect.RelayClient != "available" || status.Connect.Reachable || !strings.Contains(status.Connect.Reachability, "7844") {
		t.Fatalf("connected status=%+v", status.Connect)
	}
	run.connectStatus = strings.Replace(run.connectStatus, `{"status":"unreachable","reason":"cannot reach region1.v2.argotunnel.com:7844"}`, `{"status":"reachable"}`, 1)
	if status = readFullToolStatus(t, e); status.Connect == nil || !status.Connect.Reachable {
		t.Fatalf("reachable status=%+v", status.Connect)
	}
	run.connectStatusFailure = true
	status = readFullToolStatus(t, e)
	if status.Connect == nil || !strings.Contains(status.Connect.Error, "status unavailable: status failed") || status.ServiceState != "running" {
		t.Fatalf("status with a failing connect query=%+v", status)
	}
	run.connectStatusFailure = false
	if err := os.Remove(filepath.Join(toolDirectory(home), "home", "runtime", "versions", "0.1.2", ".install-complete")); err != nil {
		t.Fatal(err)
	}
	status = readFullToolStatus(t, e)
	if status.Connect == nil || !strings.Contains(status.Connect.Error, "the installation is damaged") {
		t.Fatalf("status on a damaged installation ran the launcher or hid it: %+v", status.Connect)
	}
	var statuses []manager.ToolStatus
	if err := json.Unmarshal([]byte(runTools(t, e, 0, "list")), &statuses); err != nil || len(statuses) != 1 || statuses[0].Connect != nil {
		t.Fatalf("list carries the connect part or failed: %+v error=%v", statuses, err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(runTools(t, e, 0, "status", "t3code", "--connect")), &raw); err != nil || raw["connect"] == nil {
		t.Fatalf("status JSON lacks connect: %v %v", raw, err)
	}
}
