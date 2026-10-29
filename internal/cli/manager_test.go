package cli_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestManagerQueryBuilderRejectsInvalidRequests proves that unsafe targets, unknown operations, and invalid payloads yield no Podman arguments through the host manager argument builder.
func TestManagerQueryBuilderRejectsInvalidRequests(t *testing.T) {
	for _, request := range [][3]string{
		{"", "/manager", "version"}, {"--privileged", "/manager", "version"},
		{"demo\nother", "/manager", "version"}, {"demo\x00other", "/manager", "version"},
		{"demo", "", "version"}, {"demo", "relative-manager", "version"},
		{"demo", "/manager\x00other", "version"}, {"demo", "/manager", "work"},
	} {
		args, err := cli.ManagerQueryArgs(request[0], request[1], request[2])
		if err == nil || len(args) != 0 {
			t.Errorf("request=%q arguments=%q error=%v; want rejection without arguments", request, args, err)
		}
	}
	for _, request := range []struct {
		command string
		payload []string
	}{{"version", []string{"extra"}}, {"ssh-host-key", []string{"extra"}}, {"ssh-deauthorize", []string{"extra"}}, {"ssh-authorize", nil}, {"ssh-authorize", []string{"ssh-ed25519 AAAA"}}, {"ssh-authorize", []string{"one", "two"}}} {
		args, err := cli.ManagerQueryArgs("demo", "/manager", request.command, request.payload...)
		if err == nil || len(args) != 0 {
			t.Errorf("request=%+v arguments=%v error=%v", request, args, err)
		}
	}
}

// TestManagerQueriesExplicitlyRequestContainerRoot proves that administrative session and SSH requests name root and preserve validated arguments through the host manager argument builder.
func TestManagerQueriesExplicitlyRequestContainerRoot(t *testing.T) {
	for _, subcommand := range []string{"version", "sessions", "ssh-host-key", "ssh-deauthorize", "ssh-authorize"} {
		t.Run(subcommand, func(t *testing.T) {
			var payload []string
			if subcommand == "ssh-authorize" {
				payload = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
			}
			got, err := cli.ManagerQueryArgs("sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand, payload...)
			want := []string{"exec", "--user=0:0", "sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand}
			want = append(want, payload...)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments=%q error=%v; want %q", got, err, want)
			}
		})
	}
}

// TestAdministrativeStatusWaitsForSlowSuccessfulExit proves a complete agent status is used only
// after a transport taking longer than three seconds succeeds, through RunWithHost and real fixture processes.
func TestAdministrativeStatusWaitsForSlowSuccessfulExit(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"opencode"] = response{Output: `{"name":"opencode","enabled":true,"installed_version":"1.19.4","session":true}`, DelayAfterOutputMS: 3300}
	start := time.Now()
	status, output, _ := f.run(t, "agents", "status", "agent01", "opencode")
	if status != 0 || !strings.Contains(output, "session true") || time.Since(start) < 3*time.Second {
		t.Fatalf("status=%d after %v output=%s", status, time.Since(start), output)
	}
}

// TestAdministrativeReadRetriesTransportOnce proves a failed read's partial JSON is discarded
// and one successful retry supplies the answer without warnings through the CLI process seam.
func TestAdministrativeReadRetriesTransportOnce(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	key := agentWorkStatus + "opencode"
	f.responses[key] = response{Sequence: []response{
		{Output: `{"name":"WRONG",`, Stderr: "transport failed", Exit: 125},
		{Output: `{"name":"opencode","enabled":true,"installed_version":"1.19.4","session":true}`},
	}}
	status, out, diagnostic, calls := runCommand(t, f, "agents", "status", "agent01", "opencode")
	if status != 0 || !strings.Contains(out, "session true") || diagnostic != "" || countManagerCalls(calls, key) != 2 {
		t.Fatalf("status=%d stdout=%s stderr=%s calls=%v", status, out, diagnostic, calls)
	}
}

// countManagerCalls counts requests observed by the external Podman fixture.
func countManagerCalls(calls [][]string, key string) int {
	n := 0
	for _, call := range calls {
		if strings.Join(call, " ") == key {
			n++
		}
	}
	return n
}

// TestAdministrativeReadRefusesFailedOrInvalidAnswers proves manager operation failures are not
// retried, transport failures retry at most once, and valid failed JSON never authorizes status at the CLI seam.
func TestAdministrativeReadRefusesFailedOrInvalidAnswers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exit, calls  int
		answer, kind string
	}{
		{"operation", 1, 1, `{"name":"opencode","enabled":true,"installed_version":"1.19.4"}`, "manager operation failed"},
		{"transport", 125, 2, `{"name":"opencode","enabled":true,"installed_version":"1.19.4"}`, "transport failed"},
		{"invalid-success", 0, 1, `{"name":`, "invalid agent status answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			key := agentWorkStatus + "opencode"
			f.responses[key] = response{Output: tc.answer, Exit: tc.exit}
			status, out, diagnostic, calls := runCommand(t, f, "agents", "status", "agent01", "opencode")
			if status == 0 || out != "" || !strings.Contains(diagnostic, tc.kind) || countManagerCalls(calls, key) != tc.calls {
				t.Fatalf("status=%d stdout=%s stderr=%s calls=%v", status, out, diagnostic, calls)
			}
			if tc.exit != 0 && (!strings.Contains(diagnostic, "after ") || !strings.Contains(diagnostic, "total budget 15s")) {
				t.Fatal(diagnostic)
			}
		})
	}
}

// TestAdministrativeHangExhaustsOneTotalBudget proves a read emitting valid JSON but never
// completing retries once and fails within one bounded budget through the external CLI process seam.
func TestAdministrativeHangExhaustsOneTotalBudget(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	key := agentWorkStatus + "opencode"
	f.responses[key] = response{Output: `{"name":"opencode","enabled":true,"installed_version":"1.19.4"}`, DelayAfterOutputMS: 30000}
	start := time.Now()
	status, out, diagnostic, calls := runCommand(t, f, "agents", "status", "agent01", "opencode")
	elapsed := time.Since(start)
	if status == 0 || out != "" || !strings.Contains(diagnostic, "deadline exceeded") || !strings.Contains(diagnostic, "2 attempt(s)") || countManagerCalls(calls, key) != 2 || elapsed > 17*time.Second || elapsed < 12*time.Second {
		t.Fatalf("status=%d elapsed=%v stdout=%s stderr=%s calls=%v", status, elapsed, out, diagnostic, calls)
	}
}

// TestNormalRemoveRefusesUnknownSessionsAfterRetry proves failed session reads never permit
// normal removal or volume deletion, through RunWithHost and the external Podman fixture.
func TestNormalRemoveRefusesUnknownSessionsAfterRetry(t *testing.T) {
	f := sandboxStateFixture(t)
	key := "exec --user=0:0 " + shellContainer + " " + integrationManager + " sessions"
	f.responses[key] = response{Exit: 125}
	status, output, calls := f.run(t, "remove", "agent01", "--volumes")
	if status == 0 || !strings.Contains(output, "cannot rule out running sessions") || countManagerCalls(calls, key) != 2 {
		t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}
