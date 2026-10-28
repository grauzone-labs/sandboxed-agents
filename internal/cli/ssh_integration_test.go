package cli_test

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// enableSSHConfig exposes the staged workflow only within a test of the public CLI boundary.
func enableSSHConfig(t *testing.T) {
	t.Helper()
	old := catalog.Default.Integrations["ssh"]
	integration := old
	workflow := integration.ConfigWorkflows[""]
	workflow.Delivered = true
	integration.ConfigWorkflows = map[string]catalog.ConfigWorkflow{"": workflow}
	catalog.Default.Integrations["ssh"] = integration
	t.Cleanup(func() { catalog.Default.Integrations["ssh"] = old })
}

// TestSSHConfigValidatesDestination proves required literal destinations and usernames are validated and normalized through cli.Parse before any effects.
func TestSSHConfigValidatesDestination(t *testing.T) {
	enableSSHConfig(t)
	for _, tc := range []struct{ host, user, want string }{
		{"192.0.2.7", "alice", "192.0.2.7"},
		{"2001:0DB8:0:0:0:0:0:1", "alice", "2001:db8::1"},
		{"Build.Example", "alice", "build.example"},
		{"worker", "DOMAIN\\alice", "worker"},
		{"", "alice", ""}, {"worker", "", ""},
		{"-oProxyCommand=bad", "alice", ""}, {"bad\nhost", "alice", ""},
		{"bad host", "alice", ""}, {"user@worker", "alice", ""},
		{"https://worker", "alice", ""}, {"worker:2222", "alice", ""},
		{"999.1.1.1", "alice", ""}, {"2001:::1", "alice", ""},
		{"bad..name", "alice", ""}, {"worker", "bad\nuser", ""},
		{"worker", "-root", ""}, {"worker", "a user", ""},
	} {
		t.Run(tc.host+"/"+tc.user, func(t *testing.T) {
			args := []string{"integrations", "config", "demo", "ssh"}
			if tc.host != "" {
				args = append(args, "--host", tc.host)
			}
			if tc.user != "" {
				args = append(args, "--user", tc.user)
			}
			r, err := cli.Parse(args, "default")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted malformed destination: %+v", r)
				}
				return
			}
			if err != nil || r.Options["host"] != tc.want || r.Options["user"] != tc.user {
				t.Fatalf("request=%+v error=%v; want host %s", r, err, tc.want)
			}
		})
	}
	for _, args := range [][]string{
		{"integrations", "config", "demo", "ssh", "extra", "--host", "worker", "--user", "alice"},
		{"integrations", "config", "demo", "ssh", "--host", "worker", "--user", "alice", "--email", "x"},
	} {
		if _, err := cli.Parse(args, "default"); err == nil {
			t.Fatalf("accepted %s", strings.Join(args, " "))
		}
	}
}

// TestSSHConfigRequiresTerminalAndRunsAsAgent proves the staged key workflow needs both terminal streams and forwards only explicit values as agent through the host CLI/fake-Podman seam.
func TestSSHConfigRequiresTerminalAndRunsAsAgent(t *testing.T) {
	enableSSHConfig(t)
	for _, terminals := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(terminals), func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[integrationProbe] = response{}
			f.host.IsInputTerminal = func(io.Reader) bool { return terminals[0] }
			f.host.IsOutputTerminal = func(io.Writer) bool { return terminals[1] }
			f.host.ReadFile = func(string) ([]byte, error) { t.Fatal("read host data"); return nil, nil }
			f.host.CurrentUser = func() (*user.User, error) { t.Fatal("read host user"); return nil, nil }
			state := filepath.Join(t.TempDir(), "unused-state")
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("LOCALAPPDATA", state)
			workflow := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", "-it", shellContainer, integrationManager, "integrations", "config", "ssh", "--host", "2001:db8::1", "--user", "alice"}
			f.responses[strings.Join(workflow, " ")] = response{}
			status, output, calls := f.run(t, "integrations", "config", "agent01", "ssh", "--host", "2001:0db8::1", "--user", "alice")
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
			if terminals[0] && terminals[1] {
				want = append(want, workflow)
				if status != 0 {
					t.Fatalf("status=%d output=%s", status, output)
				}
			} else if status != 1 || !strings.Contains(output, "interactive terminal") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%q want=%q", calls, want)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("host state was accessed/created: %v", err)
			}
		})
	}
}

// TestSSHConfigRefusalsPrecedeTerminal proves existing sandbox and manager refusals keep their order and messages through the host CLI/fake-Podman seam.
func TestSSHConfigRefusalsPrecedeTerminal(t *testing.T) {
	enableSSHConfig(t)
	for _, tc := range []struct {
		name      string
		configure func(*fixture)
		messages  []string
		manager   bool
	}{
		{"unknown", func(f *fixture) { f.responses["container exists "+shellContainer] = response{Exit: 1} }, []string{"does not exist"}, false},
		{"volumes-only", func(f *fixture) {
			f.responses["container exists "+shellContainer] = response{Exit: 1}
			f.responses["volume exists "+sandboxVolumes[1]] = response{}
		}, []string{"up agent01"}, false},
		{"owner", func(f *fixture) { f.responses[shellInspect] = response{Output: containerInspectAnswer(`{}`, "false")} }, []string{"owner conflict"}, false},
		{"interrupted", func(f *fixture) { f.responses["container exists "+sandboxBackup] = response{} }, []string{"update agent01"}, false},
		{"stopped", func(f *fixture) {
			f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
		}, []string{"start agent01"}, false},
		{"manager", func(f *fixture) { f.responses[integrationProbe] = response{Exit: 125} }, []string{"manager does not answer", "check agent01", "restart agent01"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			tc.configure(f)
			f.host.IsInputTerminal = func(io.Reader) bool { t.Fatal("checked terminal before state refusal"); return false }
			status, output, calls := f.run(t, "integrations", "config", "agent01", "ssh", "--host", "worker", "--user", "alice")
			if status != 1 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			for _, m := range tc.messages {
				if !strings.Contains(output, m) {
					t.Errorf("output=%s missing %s", output, m)
				}
			}
			for _, call := range calls {
				if call[0] == "exec" && (!tc.manager || strings.Join(call, " ") != integrationProbe) {
					t.Fatalf("unexpected exec %q", call)
				}
			}
		})
	}
}

// TestGitConfigRejectsStagedSSHOptions proves staged SSH options remain unknown for delivered Git workflows without Podman calls through cli.Parse and the host CLI boundary.
func TestGitConfigRejectsStagedSSHOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"identity-host", []string{"git", "identity", "--name", "A", "--email", "B", "--host", "x"}, `unknown option "--host"`},
		{"credentials-user", []string{"git", "credentials", "--user", "x"}, `unknown option "--user"`},
		{"host-before-workflow", []string{"git", "--host=x", "identity", "--name", "A", "--email", "B"}, `unknown option "--host=x"`},
		{"user-before-integration", []string{"--user", "x", "git", "credentials"}, `unknown option "--user"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"integrations", "config", "agent01"}, tc.args...)
			if _, err := cli.Parse(args, "default"); err == nil || err.Error() != tc.want {
				t.Errorf("parse error=%v; want %s", err, tc.want)
			}
			f := sandboxStateFixture(t)
			status, output, calls := f.run(t, args...)
			if status == 0 || len(calls) != 0 || !strings.Contains(output, tc.want) {
				t.Fatalf("status=%d calls=%q output=%s; want %s", status, calls, output, tc.want)
			}
		})
	}
}

// TestSSHConfigStepOneAndDeliveryGateCallNoPodman proves invalid inputs and the undelivered default refuse before external calls through the host CLI boundary.
func TestSSHConfigStepOneAndDeliveryGateCallNoPodman(t *testing.T) {
	f := sandboxStateFixture(t)
	status, output, calls := f.run(t, "integrations", "config", "agent01", "ssh", "--host", "worker", "--user", "alice")
	if status == 0 || len(calls) != 0 || !strings.Contains(output, `unknown integration "ssh"`) {
		t.Fatalf("status=%d calls=%q output=%s", status, calls, output)
	}
	enableSSHConfig(t)
	for _, tail := range [][]string{{"--host", "worker"}, {"--user", "alice"}, {"--host", "bad host", "--user", "alice"}, {"--host", "worker", "--user", "bad\nuser"}} {
		f := sandboxStateFixture(t)
		status, output, calls := f.run(t, append([]string{"integrations", "config", "agent01", "ssh"}, tail...)...)
		if status == 0 || len(calls) != 0 {
			t.Fatalf("status=%d calls=%q output=%s", status, calls, output)
		}
	}
}

// TestSSHConfigSelectsOneWindowsTarget proves the staged workflow binds every sandbox query and both manager identities to one machine through the host CLI seam.
func TestSSHConfigSelectsOneWindowsTarget(t *testing.T) {
	enableSSHConfig(t)
	f := windowsHost(t)
	configureSandboxQueries(f, "default", "--connection chosen ")
	f.responses["--connection chosen container exists "+shellContainer] = response{}
	f.responses["--connection chosen "+integrationProbe] = response{}
	f.host.IsInputTerminal = func(io.Reader) bool { return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	workflow := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -it " + shellContainer + " " + integrationManager + " integrations config ssh --host worker --user alice")
	f.responses["--connection chosen "+strings.Join(workflow, " ")] = response{}
	status, output, calls := f.run(t, "integrations", "config", "agent01", "ssh", "--host", "worker", "--user", "alice")
	want := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
	for _, call := range append(sandboxQueryCalls(), strings.Fields(integrationProbe), workflow) {
		want = append(want, append([]string{"--connection", "chosen"}, call...))
	}
	if status != 0 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%s calls=%q", status, output, calls)
	}
}
