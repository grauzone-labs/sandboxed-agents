package manager_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// pairedTool enables T3 Code with a running service and a signed-in tailnet node, with the caller's standard input attached.
func pairedTool(t *testing.T) (string, *toolRun, manager.Effects, *fakeServices) {
	t.Helper()
	home, run, e, services := enabledTool(t)
	e.Stdin = strings.NewReader("")
	runTools(t, e, 0, "tailscale", "t3code", "login", "--hostname", "agent01")
	return home, run, e, services
}

// stopNodeSupervisor ends the tailnet node's supervisor alone, so the service keeps running while the node is stopped.
func stopNodeSupervisor(t *testing.T, home string, services *fakeServices) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tailnetPath(home), "service", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		SupervisorPID int `json:"supervisor_pid"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if err := services.signal(state.SupervisorPID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	services.mu.Lock()
	done := services.done[state.SupervisorPID]
	services.mu.Unlock()
	<-done
}

// pairCalls lists the fake pairing calls by their first two words.
func pairCalls(run *toolRun) []string {
	var calls []string
	for _, call := range run.pairing.calls {
		calls = append(calls, strings.Join(call[:min(len(call), 2)], " "))
	}
	return calls
}

// TestToolsPairIssuesACodeForTheTailnetOriginOnly proves pair runs upstream's `t3 pair --tailscale` as agent with the Tool's tailscale command first on its PATH and the caller's terminal, maps the node's HTTPS port 443 to the service with `tailscale serve` before upstream runs and only when no such mapping exists, passes `--ttl` through and prints the expiry it moves, prints upstream's QR code, URL, and expiry followed by the single-use note, records the Serve mapping and the paired devices in the status, takes no manager lock, and that `--revoke` revokes every unused code and then finds none, at RunWithEffects.
func TestToolsPairIssuesACodeForTheTailnetOriginOnly(t *testing.T) {
	home, run, e, _ := pairedTool(t)
	release, held, err := lockFor(t, home)
	if err != nil || !held {
		t.Fatalf("lock: held=%v err=%v", held, err)
	}
	out := runTools(t, e, 0, "pair", "t3code")
	release()
	for _, want := range []string{"█▀▀▀▀▀█ QR █▀▀▀▀▀█", "Pairing URL: https://agent01.tail1234.ts.net/pair#token=TOKEN1", "Expires: 2026-10-23T12:05:00.000Z", "The code above is single-use and valid until the printed expiry; this command does not confirm the scan. Paired devices appear in tools status NAME t3code."} {
		if !strings.Contains(out, want) {
			t.Fatalf("pair output lacks %q:\n%s", want, out)
		}
	}
	if !slices.Equal(pairCalls(run), []string{"auth pairing", "pair --tailscale"}) || run.pairing.interactivePairs != 1 || run.interruptWatches != 2 {
		t.Fatalf("pair calls=%v interactive=%d watches=%d", run.pairing.calls, run.pairing.interactivePairs, run.interruptWatches)
	}
	serveCall := []string{"serve", "--bg", "--https=443", "http://127.0.0.1:3773"}
	if serves := slices.IndexFunc(run.tailnet.calls, func(args []string) bool { return slices.Equal(args, serveCall) }); serves < 0 || slices.ContainsFunc(run.tailnet.calls[serves+1:], func(args []string) bool { return slices.Equal(args, serveCall) }) {
		t.Fatalf("pair did not map the origin once before upstream ran: %v", run.tailnet.calls)
	}
	serveCalls := len(run.tailnet.calls)
	path := ""
	for _, entry := range run.pairing.environments[1] {
		if strings.HasPrefix(entry, "PATH=") {
			path = entry
		}
	}
	if !strings.HasPrefix(path, "PATH="+filepath.Join(toolDirectory(home), "bin")+":") {
		t.Fatalf("pair PATH=%q does not start with the Tool's bin directory", path)
	}
	if out := runTools(t, e, 0, "pair", "t3code", "--ttl", "90m"); !strings.Contains(out, "TOKEN2") || !strings.Contains(out, "Expires: 2026-10-23T13:30:00.000Z") || !slices.Equal(run.pairing.calls[len(run.pairing.calls)-1], []string{"pair", "--tailscale", "--ttl", "90m"}) {
		t.Fatalf("pair with ttl output=%s calls=%v", out, run.pairing.calls)
	}
	if slices.ContainsFunc(run.tailnet.calls[serveCalls:], func(args []string) bool { return slices.Equal(args, serveCall) }) {
		t.Fatalf("pair remapped an origin that already fronts the service: %v", run.tailnet.calls[serveCalls:])
	}
	run.pairing.sessions = `[{"sessionId":"s1","method":"pairing","scopes":["threads:read"],"subject":"one-time-token","client":{"label":"Pixel 8","deviceType":"mobile","os":"Android"},"connected":true,"issuedAt":"2026-10-23T12:01:00.000Z","expiresAt":"2026-11-22T12:01:00.000Z","lastConnectedAt":"2026-10-23T12:02:00.000Z"},{"sessionId":"s2","method":"pairing","scopes":[],"subject":"one-time-token","client":{"deviceType":"tablet","os":"iPadOS"},"connected":false,"issuedAt":"2026-10-23T12:03:00.000Z","expiresAt":"2026-11-22T12:03:00.000Z","lastConnectedAt":null}]`
	full := readFullToolStatus(t, e)
	if full.Tailnet == nil || full.Tailnet.Serve != "http://127.0.0.1:3773" || !full.Tailnet.ServesService || full.Tailnet.Origin != "https://agent01.tail1234.ts.net/" {
		t.Fatalf("tailnet status=%+v", full.Tailnet)
	}
	wantDevices := &catalog.DeviceReport{Items: []catalog.PairedDevice{{Client: "Pixel 8", Connected: true, LastConnected: "2026-10-23T12:02:00.000Z"}, {Client: "tablet (iPadOS)"}}}
	if full.Devices == nil || full.Devices.Error != "" || !slices.Equal(full.Devices.Items, wantDevices.Items) {
		t.Fatalf("devices=%+v want %+v", full.Devices, wantDevices)
	}
	var plain manager.ToolStatus
	decode(t, runTools(t, e, 0, "status", "t3code"), &plain)
	if plain.Devices != nil {
		t.Fatalf("status without --connect listed devices: %+v", plain.Devices)
	}
	if out := runTools(t, e, 0, "pair", "t3code", "--revoke"); !strings.Contains(out, "t3code: revoked 2 unused pairing codes") || len(run.pairing.codes) != 0 {
		t.Fatalf("revoke output=%s codes=%v", out, run.pairing.codes)
	}
	if out := runTools(t, e, 0, "pair", "t3code", "--revoke"); !strings.Contains(out, "t3code: no unused pairing code") {
		t.Fatalf("second revoke output=%s", out)
	}
	run.pairing.sessionsResult = childExit(1)
	if got := readFullToolStatus(t, e); got.Devices == nil || got.Devices.Error != "devices unavailable: database locked" {
		t.Fatalf("devices with a failing list=%+v", got.Devices)
	}
}

// TestToolsPairRefusesWithDistinctMessagesAndIssuesNoCode proves a Tool that is not enabled, a stopped service, a missing, stopped, or signed-out tailnet node, a missing MagicDNS name, disabled HTTPS certificates, an offline node, a Serve mapping that fronts another target, an upstream failure, Ctrl+C, a `tailscale serve` that fails or whose mapping does not take, both before upstream runs, an upstream that minted a code and then failed, and a mapping upstream lost each exit non-zero with their own message, that none of them leaves a code this pairing issued, and that earlier codes stay, at RunWithEffects.
func TestToolsPairRefusesWithDistinctMessagesAndIssuesNoCode(t *testing.T) {
	disabled, disabledRun := t.TempDir(), &toolRun{}
	if out := runTools(t, toolEffects(t, disabled, toolRegistry{}, disabledRun), 1, "pair", "t3code"); !strings.Contains(out, "t3code not enabled; run tools enable NAME t3code") {
		t.Fatalf("disabled pair output=%s", out)
	}
	for _, tc := range []struct {
		name    string
		prepare func(home string, run *toolRun, e manager.Effects, services *fakeServices)
		message string
		// upstream is true when the refusal comes after upstream's pair ran.
		upstream bool
	}{
		{"no-node", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			runTools(t, e, 0, "tailscale", "t3code", "logout")
		}, "t3code tailnet node not signed in; run tools tailscale NAME t3code login", false},
		{"service-stopped", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			stopFakeSupervisors(t, services)
		}, "t3code service is stopped; run tools enable NAME t3code to start it, or tools status NAME t3code for its failure", false},
		{"node-stopped", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			stopNodeSupervisor(t, home, services)
		}, "t3code tailnet node is stopped; run tools enable NAME t3code to start it, or tools status NAME t3code for its failure", false},
		{"signed-out", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.tailnet.loggedIn = false
		}, "t3code tailnet node not signed in (NeedsLogin); run tools tailscale NAME t3code login", false},
		{"no-magicdns", func(home string, run *toolRun, e manager.Effects, services *fakeServices) { run.tailnet.name = "." }, "the tailnet has no MagicDNS name; enable MagicDNS at https://login.tailscale.com/admin/dns, then run tools pair NAME t3code again", false},
		{"no-https", func(home string, run *toolRun, e manager.Effects, services *fakeServices) { run.tailnet.noHTTPS = true }, "HTTPS certificates are not enabled on the tailnet; enable them at https://login.tailscale.com/admin/dns, then run tools pair NAME t3code again", false},
		{"offline", func(home string, run *toolRun, e manager.Effects, services *fakeServices) { run.tailnet.offline = true }, "t3code tailnet node is offline; check the sandbox's outbound connectivity and tools status NAME t3code", false},
		{"foreign-serve", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.tailnet.serve = `{"Web":{"agent01.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:9999"}}}}}`
		}, "the tailnet node's HTTPS port 443 already fronts http://127.0.0.1:9999, not this service; run tools tailscale NAME t3code logout and then login to reset the node", false},
		{"status-unavailable", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.tailnet.statusResult = childExit(1)
		}, "t3code tailnet node status unavailable: failed to connect to local tailscaled", false},
		{"upstream-failure", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.pairing.pairResult, run.pairing.pairText = childExit(1), "tailscale serve failed for HTTPS port 443."
		}, "t3code pair failed with exit status 1", true},
		{"interrupted", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.pairing.interruptPair = true
		}, "t3code pair was interrupted", true},
		{"upstream-failure-after-issue", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.pairing.codes, run.pairing.pairResult, run.pairing.pairText, run.pairing.issueBeforeFailure = []string{"pc_earlier"}, childExit(1), "died after minting", true
		}, "t3code pair failed with exit status 1; the code it issued was revoked", true},
		{"serve-failure", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.tailnet.serveResult = childExit(1)
		}, "t3code pair: tailscale serve failed: serve config denied; no code was issued; see tools status NAME t3code", false},
		{"mapping-not-taken", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			run.tailnet.serveIgnored = true
		}, "t3code pair: the tailnet node does not front the service after tailscale serve; no code was issued; see tools status NAME t3code", false},
		{"mapping-lost-after-pair", func(home string, run *toolRun, e manager.Effects, services *fakeServices) {
			// An earlier, still valid code must survive the failure; only the code this pairing issued goes.
			run.pairing.codes, run.pairing.clearServe = []string{"pc_earlier"}, true
		}, "t3code pair: the tailnet node does not front the service after the pairing; the issued code was revoked; see tools status NAME t3code", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, run, e, services := pairedTool(t)
			tc.prepare(home, run, e, services)
			kept := slices.Clone(run.pairing.codes)
			out := runTools(t, e, 1, "pair", "t3code")
			if !strings.Contains(out, tc.message) {
				t.Fatalf("output=%s want %q", out, tc.message)
			}
			if tc.name == "upstream-failure" && !strings.Contains(out, "tailscale serve failed for HTTPS port 443.") {
				t.Fatalf("upstream text missing: %s", out)
			}
			if !slices.Equal(run.pairing.codes, kept) {
				t.Fatalf("pairing codes=%v want %v", run.pairing.codes, kept)
			}
			if ran := slices.Contains(pairCalls(run), "pair --tailscale"); ran != tc.upstream {
				t.Fatalf("upstream pair ran=%v want %v: %v", ran, tc.upstream, run.pairing.calls)
			}
		})
	}
}

// TestToolsPairRevokeWorksWithoutTheServiceAndReportsListFailures proves `--revoke` acts on the database with the service and the node stopped, reports a list that cannot be read, and takes no terminal, at RunWithEffects.
func TestToolsPairRevokeWorksWithoutTheServiceAndReportsListFailures(t *testing.T) {
	home, run, e, services := pairedTool(t)
	runTools(t, e, 0, "pair", "t3code")
	stopFakeSupervisors(t, services)
	e.Stdin = nil
	if out := runTools(t, e, 0, "pair", "t3code", "--revoke"); !strings.Contains(out, "t3code: revoked 1 unused pairing code") || len(run.pairing.codes) != 0 {
		t.Fatalf("revoke output=%s codes=%v", out, run.pairing.codes)
	}
	run.pairing.listResult = childExit(1)
	if out := runTools(t, e, 1, "pair", "t3code", "--revoke"); !strings.Contains(out, "t3code pair --revoke: cannot list the pairing codes: database locked") {
		t.Fatalf("failed list output=%s", out)
	}
	if _, err := os.Stat(filepath.Join(toolDirectory(home), "home")); err != nil {
		t.Fatal(err)
	}
}
