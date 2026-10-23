package manager_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// decode parses the JSON a manager command printed into the target and fails the test on an error.
func decode(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("output %q: %v", output, err)
	}
}

// tailnetPath is the `tailscale/` subdirectory of the Tool directory in a test home.
func tailnetPath(home string) string {
	return filepath.Join(toolDirectory(home), "tailscale")
}

// tailnetSupervisors counts the supervisors detached for the tailnet node.
func tailnetSupervisors(services *fakeServices) int {
	count := 0
	for _, args := range services.detachArgs {
		if slices.Equal(args, []string{"__supervise", "t3code", "tailnet"}) {
			count++
		}
	}
	return count
}

// TestToolsTailscaleLoginCreatesAndSignsInTheTailnetNodeAsAgent proves login starts one supervised tailscaled in userspace networking mode as agent with its state and socket in the Tool directory, writes the Tool's tailscale command wrapper, runs `tailscale up` interactively with the sandbox name as hostname, reports the MagicDNS name and origin, exposes the node in the status (its sign-in with `--connect` or `--tailnet` only), restores the node with the service, refuses under the manager lock, and that a repeated login changes nothing, at RunWithEffects with real locks.
func TestToolsTailscaleLoginCreatesAndSignsInTheTailnetNodeAsAgent(t *testing.T) {
	home, run, e, services := enabledTool(t)
	e.Stdin = strings.NewReader("")
	before := homeSnapshot(t, home)
	out := runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01")
	if !strings.Contains(out, "To authenticate, visit:") || !strings.Contains(out, "https://login.tailscale.com/a/0123abcd4567") || !strings.Contains(out, "t3code tailnet node signed in as agent01.tail1234.ts.net; origin https://agent01.tail1234.ts.net/") {
		t.Fatalf("login output=%s", out)
	}
	if tailnetSupervisors(services) != 1 {
		t.Fatalf("supervisors=%v", services.detachArgs)
	}
	want := []string{"/usr/sbin/tailscaled", "--tun=userspace-networking", "--statedir=" + filepath.Join(tailnetPath(home), "state"), "--socket=" + filepath.Join(tailnetPath(home), "tailscaled.sock"), "--no-logs-no-support"}
	if !slices.ContainsFunc(services.spawnArgs, func(args []string) bool { return slices.Equal(args, want) }) {
		t.Fatalf("tailscaled spawn=%v want %v", services.spawnArgs, want)
	}
	up := []string{"up", "--hostname=agent01", "--accept-dns=false", "--accept-routes=false"}
	if !slices.ContainsFunc(run.tailnet.calls, func(args []string) bool { return slices.Equal(args, up) }) || run.tailnet.interactiveUps != 1 || run.interruptWatches != 1 {
		t.Fatalf("tailscale calls=%v interactive=%d watches=%d", run.tailnet.calls, run.tailnet.interactiveUps, run.interruptWatches)
	}
	wrapper, err := os.ReadFile(filepath.Join(toolDirectory(home), "bin", "tailscale"))
	if err != nil || !strings.Contains(string(wrapper), "exec /usr/bin/tailscale --socket="+filepath.Join(tailnetPath(home), "tailscaled.sock")+" \"$@\"") {
		t.Fatalf("wrapper=%q error=%v", wrapper, err)
	}
	if info, err := os.Stat(filepath.Join(toolDirectory(home), "bin", "tailscale")); err != nil || info.Mode().Perm()&0100 == 0 {
		t.Fatalf("wrapper not executable: %v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(tailnetPath(home), "state")); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("state directory=%v error=%v", info, err)
	}
	if after := homeSnapshot(t, home); !equalSnapshots(before, after) {
		t.Fatal("login changed files outside the Tool directory")
	}
	full := readFullToolStatus(t, e)
	wantTailnet := &catalog.TailnetStatus{Node: "running", NodeLog: filepath.Join(tailnetPath(home), "service", "service.log"), Backend: "Running", Name: "agent01.tail1234.ts.net", Origin: "https://agent01.tail1234.ts.net/", Online: true, HTTPS: "enabled"}
	if full.Tailnet == nil || *full.Tailnet != *wantTailnet {
		t.Fatalf("tailnet status=%+v want %+v", full.Tailnet, wantTailnet)
	}
	var plain manager.ToolStatus
	decode(t, runTools(t, e, 0, "status", "t3code"), &plain)
	if plain.Tailnet == nil || *plain.Tailnet != (catalog.TailnetStatus{Node: "running", NodeLog: wantTailnet.NodeLog}) {
		t.Fatalf("status without --connect ran upstream queries: %+v", plain.Tailnet)
	}
	var nodeOnly manager.ToolStatus
	decode(t, runTools(t, e, 0, "status", "t3code", "--tailnet"), &nodeOnly)
	if nodeOnly.Tailnet == nil || *nodeOnly.Tailnet != *wantTailnet || nodeOnly.Connect != nil || nodeOnly.Devices != nil {
		t.Fatalf("status --tailnet=%+v connect=%v devices=%v", nodeOnly.Tailnet, nodeOnly.Connect, nodeOnly.Devices)
	}
	calls := len(run.tailnet.calls)
	if out := runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01"); !strings.Contains(out, "t3code tailnet node already signed in as agent01.tail1234.ts.net") || tailnetSupervisors(services) != 1 {
		t.Fatalf("repeated login output=%s supervisors=%d", out, tailnetSupervisors(services))
	}
	for _, call := range run.tailnet.calls[calls:] {
		if call[0] == "up" {
			t.Fatalf("repeated login ran up again: %v", run.tailnet.calls[calls:])
		}
	}
	stopFakeSupervisors(t, services)
	if got := readFullToolStatus(t, e); got.ServiceState != "stopped" || got.Tailnet == nil || got.Tailnet.Node != "stopped" {
		t.Fatalf("status after stop=%+v tailnet=%+v", got, got.Tailnet)
	}
	if out := runTools(t, e, 0, "restore"); !strings.Contains(out, "t3code service running") || !strings.Contains(out, "t3code tailnet node running") {
		t.Fatalf("restore output=%s", out)
	}
	if tailnetSupervisors(services) != 2 || services.runningServices() != 2 {
		t.Fatalf("restore did not start the node again: supervisors=%v running=%d", services.detachArgs, services.runningServices())
	}
	release, held, err := lockFor(t, home)
	if err != nil || !held {
		t.Fatalf("lock: held=%v err=%v", held, err)
	}
	defer release()
	if out := runTools(t, e, 1, "tailscale", "t3code", "login", "--hostname", "agent01"); !strings.Contains(out, "another agent or tool change is in progress") {
		t.Fatalf("locked login output=%s", out)
	}
	if out := runTools(t, e, 1, "tailscale", "t3code", "logout"); !strings.Contains(out, "another agent or tool change is in progress") {
		t.Fatalf("locked logout output=%s", out)
	}
}

// TestToolsTailscaleLoginFailuresLeaveNoNode proves an interrupted login, a failed `up`, a missing Tailscale installation, a disabled Tool, and an invalid hostname each exit non-zero with a distinct message, and that a login that created the node removes it again while a login on an existing node keeps it, at RunWithEffects.
func TestToolsTailscaleLoginFailuresLeaveNoNode(t *testing.T) {
	home, run, e, services := enabledTool(t)
	before := homeSnapshot(t, home)
	for _, tc := range []struct {
		name    string
		prepare func()
		args    []string
		status  int
		message string
	}{
		{"interrupted", func() { run.tailnet.interruptUp = true }, []string{"login", "--hostname", "agent01"}, 1, "t3code tailscale login was interrupted"},
		{"failed", func() { run.tailnet.upResult, run.tailnet.upText = childExit(1), "invalid hostname" }, []string{"login", "--hostname", "agent01"}, 1, "t3code tailscale login failed with exit status 1"},
		{"missing", func() { run.tailnet.missing = true }, []string{"login", "--hostname", "agent01"}, 1, "Tailscale is not installed in this sandbox's image; run build, then update NAME, then tools tailscale NAME t3code login again"},
		{"hostname", func() {}, []string{"login", "--hostname", "Agent_01"}, 2, `invalid hostname "Agent_01"`},
		{"no-hostname", func() {}, []string{"login"}, 2, "tools tailscale login requires --hostname"},
	} {
		run.tailnet = tailnetRun{}
		tc.prepare()
		out := runTools(t, e, tc.status, append([]string{"tailscale", "t3code"}, tc.args...)...)
		if !strings.Contains(out, tc.message) {
			t.Fatalf("%s: output=%s want %q", tc.name, out, tc.message)
		}
		if tc.name == "failed" && !strings.Contains(out, "invalid hostname") {
			t.Fatalf("%s: upstream text missing: %s", tc.name, out)
		}
		if _, err := os.Stat(tailnetPath(home)); !os.IsNotExist(err) {
			t.Fatalf("%s: tailnet directory remains: %v", tc.name, err)
		}
		if _, err := os.Stat(filepath.Join(toolDirectory(home), "bin", "tailscale")); !os.IsNotExist(err) {
			t.Fatalf("%s: wrapper remains: %v", tc.name, err)
		}
		if services.runningServices() != 1 {
			t.Fatalf("%s: running processes=%d", tc.name, services.runningServices())
		}
		if got := readFullToolStatus(t, e); got.Tailnet != nil {
			t.Fatalf("%s: status reports a node: %+v", tc.name, got.Tailnet)
		}
	}
	if after := homeSnapshot(t, home); !equalSnapshots(before, after) {
		t.Fatal("failed logins changed files outside the Tool directory")
	}
	run.tailnet = tailnetRun{}
	runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01")
	run.tailnet.loggedIn, run.tailnet.upResult = false, childExit(1)
	if out := runTools(t, e, 1, "tailscale", "t3code", "login", "--hostname", "agent01"); !strings.Contains(out, "failed with exit status 1") {
		t.Fatalf("second login output=%s", out)
	}
	if _, err := os.Stat(filepath.Join(tailnetPath(home), "state")); err != nil || services.runningServices() != 2 {
		t.Fatalf("a failed login on an existing node removed it: %v running=%d", err, services.runningServices())
	}
	disabled, disabledRun := t.TempDir(), &toolRun{}
	disabledEffects := toolEffects(t, disabled, toolRegistry{}, disabledRun)
	if out := runTools(t, disabledEffects, 1, "tailscale", "t3code", "login", "--hostname", "agent01"); !strings.Contains(out, "t3code not enabled; run tools enable NAME t3code") {
		t.Fatalf("disabled login output=%s", out)
	}
}

// TestToolsTailscaleLogoutRemovesTheNode proves logout signs the node out, stops its supervisor, removes the node's state, socket, and wrapper, that a logout failure is a warning and the removal proceeds, that a stopped node is started for its sign-out, and that a second logout finds nothing and exits zero, at RunWithEffects.
func TestToolsTailscaleLogoutRemovesTheNode(t *testing.T) {
	home, run, e, services := enabledTool(t)
	runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01")
	if out := runTools(t, e, 0, "tailscale", "t3code", "logout"); !strings.Contains(out, "t3code tailnet node removed") {
		t.Fatalf("logout output=%s", out)
	}
	if !slices.ContainsFunc(run.tailnet.calls, func(args []string) bool { return slices.Equal(args, []string{"logout"}) }) {
		t.Fatalf("tailscale logout not called: %v", run.tailnet.calls)
	}
	if _, err := os.Stat(tailnetPath(home)); !os.IsNotExist(err) {
		t.Fatalf("tailnet directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(toolDirectory(home), "bin", "tailscale")); !os.IsNotExist(err) {
		t.Fatalf("wrapper remains: %v", err)
	}
	if services.runningServices() != 1 || readFullToolStatus(t, e).Tailnet != nil {
		t.Fatalf("node still running or reported: running=%d", services.runningServices())
	}
	calls := len(run.tailnet.calls)
	if out := runTools(t, e, 0, "tailscale", "t3code", "logout"); !strings.Contains(out, "t3code tailnet node absent; nothing to sign out") || len(run.tailnet.calls) != calls {
		t.Fatalf("second logout output=%s calls=%v", out, run.tailnet.calls[calls:])
	}
	run.tailnet = tailnetRun{}
	runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01")
	stopFakeSupervisors(t, services)
	run.tailnet.logoutResult = childExit(1)
	out := runTools(t, e, 0, "tailscale", "t3code", "logout")
	if !strings.Contains(out, "warning: t3code tailscale logout failed: logout failed: control server unreachable; the node stays listed in the tailnet admin console until it expires or is removed there") || !strings.Contains(out, "t3code tailnet node removed") {
		t.Fatalf("failed logout output=%s", out)
	}
	if tailnetSupervisors(services) != 3 {
		t.Fatalf("stopped node was not started for its sign-out: %v", services.detachArgs)
	}
	if _, err := os.Stat(tailnetPath(home)); !os.IsNotExist(err) || services.runningServices() != 0 {
		t.Fatalf("tailnet directory remains after a failed sign-out: %v running=%d", err, services.runningServices())
	}
}

// TestToolsDeleteRemovesTheTailnetNode proves delete signs the Tool's tailnet node out as a best effort through the Tool's own socket only, stops its supervisor, and removes its state with the Serve mapping the pairing created together with the Tool directory, while a foreign Tailscale state with its own Serve mapping beside it, outside the Tool directory, stays byte for byte, at RunWithEffects.
func TestToolsDeleteRemovesTheTailnetNode(t *testing.T) {
	home, run, e, services := pairedTool(t)
	runTools(t, e, 0, "pair", "t3code")
	if run.tailnet.serve == "" {
		t.Fatal("the pairing created no Serve mapping")
	}
	// The node's Serve configuration lives in its state; the fake records it there as Tailscale would.
	if err := os.WriteFile(filepath.Join(tailnetPath(home), "state", "tailscaled.state"), []byte(run.tailnet.serve), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, ".local", "share", "tailscale", "tailscaled.state")
	if err := os.MkdirAll(filepath.Dir(foreign), 0700); err != nil {
		t.Fatal(err)
	}
	foreignState := `{"Web":{"other.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8080"}}}}}`
	if err := os.WriteFile(foreign, []byte(foreignState), 0600); err != nil {
		t.Fatal(err)
	}
	before := homeSnapshot(t, home)
	calls := len(run.tailnet.calls)
	if out := runTools(t, e, 0, "delete", "t3code"); !strings.Contains(out, "t3code deleted") || !strings.Contains(out, "the tailnet node") {
		t.Fatalf("delete output=%s", out)
	}
	// Every fake tailscale call names the Tool's socket, so no call reached another node; the only change is the sign-out.
	if got := run.tailnet.calls[calls:]; len(got) != 1 || !slices.Equal(got[0], []string{"logout"}) {
		t.Fatalf("delete tailscale calls=%v", run.tailnet.calls[calls:])
	}
	if _, err := os.Stat(toolDirectory(home)); !os.IsNotExist(err) || services.runningServices() != 0 {
		t.Fatalf("tool directory remains or processes run: %v running=%d", err, services.runningServices())
	}
	after := homeSnapshot(t, home)
	delete(before, filepath.Join(home, ".local", "bin", "t3"))
	delete(after, filepath.Join(home, ".local", "bin", "t3"))
	if !equalSnapshots(before, after) || after[foreign] != foreignState {
		t.Fatalf("delete changed files outside the Tool directory, the foreign Tailscale state included")
	}
}
