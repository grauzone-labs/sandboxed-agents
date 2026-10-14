package cli_test

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

const integrationManager = "/usr/local/bin/sandboxed-agents-manager"
const integrationProbe = "exec --user=0:0 " + shellContainer + " " + integrationManager + " sessions"

// TestGitConfigRefusesStoppedSandbox proves both Git workflows fail before a manager call and name start through the host CLI and fake Podman seam.
func TestGitConfigRefusesStoppedSandbox(t *testing.T) {
	for _, workflow := range []string{"identity", "credentials"} {
		t.Run(workflow, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
			status, output, calls := f.run(t, "integrations", "config", "agent01", "git", workflow)
			if status != 1 || !strings.Contains(output, "start agent01") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			if !reflect.DeepEqual(calls, sandboxQueryCalls()) {
				t.Fatalf("calls=%v; want only sandbox queries", calls)
			}
		})
	}
}

// TestGitConfigRefusesUnavailableManager proves both workflows name diagnosis and restart without a fallback at the host CLI and fake Podman seam.
func TestGitConfigRefusesUnavailableManager(t *testing.T) {
	for _, workflow := range []string{"identity", "credentials"} {
		t.Run(workflow, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[integrationProbe] = response{Exit: 125, Stderr: "manager unavailable"}
			status, output, calls := f.run(t, "integrations", "config", "agent01", "git", workflow)
			if status != 1 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			for _, message := range []string{"manager does not answer", "check agent01", "restart agent01"} {
				if !strings.Contains(output, message) {
					t.Errorf("output=%s missing=%s", output, message)
				}
			}
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

// TestGitCredentialsNeedsNoTerminalOrHostData proves the credential command executes only the manager as agent with a fixed home and no forwarded host data at the host CLI and fake Podman seam.
func TestGitCredentialsNeedsNoTerminalOrHostData(t *testing.T) {
	f := sandboxStateFixture(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/host/gitconfig")
	t.Setenv("GIT_ASKPASS", "/host/askpass")
	t.Setenv("GITHUB_TOKEN", "host-token-sentinel")
	f.responses[integrationProbe] = response{Output: "running-session\n"}
	workflow := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", shellContainer, integrationManager, "integrations", "config", "git", "credentials"}
	f.responses[strings.Join(workflow, " ")] = response{}
	status, output, calls := f.run(t, "integrations", "config", "agent01", "git", "credentials")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	want := append(sandboxQueryCalls(), strings.Fields(integrationProbe), workflow)
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

// TestGitIdentityExplicitValuesNeedNoTerminal proves explicit identity reaches the manager literally while missing values refuse without workflow execution at the host CLI seam.
func TestGitIdentityExplicitValuesNeedNoTerminal(t *testing.T) {
	for _, tc := range []struct {
		options []string
		status  int
	}{
		{[]string{"--name", "A Person", "--email", "a@example.com"}, 0},
		{[]string{"--name", "A Person"}, 1},
		{[]string{"--email", "a@example.com"}, 1},
		{nil, 1},
	} {
		t.Run(strings.Join(tc.options, " "), func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[integrationProbe] = response{}
			workflow := append([]string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", shellContainer, integrationManager, "integrations", "config", "git", "identity"}, tc.options...)
			f.responses[strings.Join(workflow, " ")] = response{}
			status, output, calls := f.run(t, append([]string{"integrations", "config", "agent01", "git", "identity"}, tc.options...)...)
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
			if tc.status == 0 {
				want = append(want, workflow)
			} else if !strings.Contains(output, "terminal") {
				t.Errorf("missing terminal diagnostic: %s", output)
			}
			if status != tc.status || !reflect.DeepEqual(calls, want) {
				t.Fatalf("status=%d output=%s calls=%v want status=%d calls=%v", status, output, calls, tc.status, want)
			}
		})
	}
}

// TestGitIdentityPromptsOnlyForMissingValues proves terminal input fills missing identity values before any Git change and incomplete input refuses at the host CLI seam.
func TestGitIdentityPromptsOnlyForMissingValues(t *testing.T) {
	for _, tc := range []struct {
		options       []string
		input, prompt string
		status        int
	}{
		{[]string{"--name", "A Person"}, "a@example.com\n", "Email: ", 0},
		{[]string{"--email", "a@example.com"}, "A Person\n", "Name: ", 0},
		{nil, "A Person\na@example.com\n", "Name: ", 0},
		{nil, "A Person\n", "Email: ", 1},
		{[]string{"--name", "A Person"}, "\n", "Email: ", 1},
	} {
		t.Run(strings.Join(tc.options, " ")+tc.input, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.host.Stdin = strings.NewReader(tc.input)
			f.host.IsInputTerminal = func(io.Reader) bool { return true }
			f.host.IsOutputTerminal = func(io.Writer) bool { return true }
			f.responses[integrationProbe] = response{}
			workflow := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", shellContainer, integrationManager, "integrations", "config", "git", "identity", "--name", "A Person", "--email", "a@example.com"}
			f.responses[strings.Join(workflow, " ")] = response{}
			status, output, calls := f.run(t, append([]string{"integrations", "config", "agent01", "git", "identity"}, tc.options...)...)
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
			if tc.status == 0 {
				want = append(want, workflow)
			}
			if status != tc.status || !strings.Contains(output, tc.prompt) || !reflect.DeepEqual(calls, want) {
				t.Fatalf("status=%d output=%s calls=%v want status=%d prompt=%s calls=%v", status, output, calls, tc.status, tc.prompt, want)
			}
		})
	}
}

// TestGitConfigSelectsOneWindowsTarget proves all sandbox queries and both workflow identities name the selected machine through the host CLI and fake Podman seam.
func TestGitConfigSelectsOneWindowsTarget(t *testing.T) {
	for _, workflow := range []string{"identity", "credentials"} {
		t.Run(workflow, func(t *testing.T) {
			f := windowsHost(t)
			configureSandboxQueries(f, "default", "--connection chosen ")
			f.responses["--connection chosen container exists "+shellContainer] = response{}
			f.responses["--connection chosen "+integrationProbe] = response{}
			execution := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", shellContainer, integrationManager, "integrations", "config", "git", workflow}
			args := []string{"integrations", "config", "agent01", "git", workflow}
			if workflow == "identity" {
				args = append(args, "--name", "N", "--email", "E")
				execution = append(execution, "--name", "N", "--email", "E")
			}
			f.responses["--connection chosen "+strings.Join(execution, " ")] = response{}
			status, output, calls := f.run(t, args...)
			want := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
			for _, call := range append(sandboxQueryCalls(), strings.Fields(integrationProbe), execution) {
				want = append(want, append([]string{"--connection", "chosen"}, call...))
			}
			if status != 0 || !reflect.DeepEqual(calls, want) {
				t.Fatalf("status=%d output=%s calls=%v want=%v", status, output, calls, want)
			}
		})
	}
}

// TestGitConfigStateChecksPrecedeTerminal proves missing sandbox, volumes only, ownership and interrupted-update failures precede terminal checks through the host CLI seam.
func TestGitConfigStateChecksPrecedeTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		configure     func(*fixture)
		calls         [][]string
	}{
		{"unknown", "does not exist", func(f *fixture) { f.responses["container exists "+shellContainer] = response{Exit: 1} }, sandboxExistenceCalls()},
		{"volumes-only", "up agent01", func(f *fixture) {
			f.responses["container exists "+shellContainer] = response{Exit: 1}
			f.responses["volume exists "+sandboxVolumes[1]] = response{}
		}, sandboxObjectCalls(false, false, sandboxVolumes[1])},
		{"owner", "owner conflict", func(f *fixture) { f.responses[shellInspect] = response{Output: containerInspectAnswer(`{}`, "false")} }, sandboxQueryCalls()},
		{"backup", "update agent01", func(f *fixture) { f.responses["container exists "+sandboxBackup] = response{} }, sandboxObjectCalls(true, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sandboxStateFixture(t)
			tc.configure(f)
			f.host.IsInputTerminal = func(io.Reader) bool { t.Error("terminal checked before sandbox state"); return false }
			status, output, calls := f.run(t, "integrations", "config", "agent01", "git", "identity")
			if status != 1 || !strings.Contains(output, tc.message) || !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("status=%d output=%s calls=%v want=%v", status, output, calls, tc.calls)
			}
		})
	}
}

// TestGitConfigWorkflowFailureIsNotPassedThrough proves workflow exit codes become a command failure without retry or fallback through the host CLI seam.
func TestGitConfigWorkflowFailureIsNotPassedThrough(t *testing.T) {
	f := sandboxStateFixture(t)
	f.responses[integrationProbe] = response{}
	workflow := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent " + shellContainer + " " + integrationManager + " integrations config git credentials")
	f.responses[strings.Join(workflow, " ")] = response{Exit: 23, Stderr: "Git failed\n"}
	status, output, calls := f.run(t, "integrations", "config", "agent01", "git", "credentials")
	want := append(sandboxQueryCalls(), strings.Fields(integrationProbe), workflow)
	if status != 1 || !strings.Contains(output, "Git failed") || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%s calls=%v want=%v", status, output, calls, want)
	}
}
