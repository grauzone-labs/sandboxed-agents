package cli_test

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func azureIntegrationFixture(t *testing.T) *fixture {
	t.Helper()
	f := sandboxStateFixture(t)
	f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}`, "true")}
	return f
}

// TestAzureDevOpsLoginReadsStdin proves piped PAT input is accepted regardless of stdout and is absent from Podman arguments and command output at the host CLI seam.
func TestAzureDevOpsLoginReadsStdin(t *testing.T) {
	for _, outputTerminal := range []bool{false, true} {
		t.Run(fmt.Sprint(outputTerminal), func(t *testing.T) {
			f := azureIntegrationFixture(t)
			f.host.Stdin = strings.NewReader("pat-host-sentinel\n")
			f.host.IsInputTerminal = func(io.Reader) bool { return false }
			f.host.IsOutputTerminal = func(io.Writer) bool { return outputTerminal }
			f.responses[integrationProbe] = response{}
			workflow := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " integrations login azdo --organization https://dev.azure.com/contoso")
			f.responses[strings.Join(workflow, " ")] = response{EchoStdin: true, ExpectedStdin: "pat-host-sentinel\n"}
			status, output, calls := f.run(t, "integrations", "login", "agent01", "azdo", "--organization", "https://dev.azure.com/contoso/")
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe), workflow)
			if status != 0 || !reflect.DeepEqual(calls, want) {
				t.Fatalf("status=%d output=%s calls=%q", status, output, calls)
			}
			if strings.Contains(output, "pat-host-sentinel") || strings.Contains(fmt.Sprint(calls), "pat-host-sentinel") {
				t.Fatal("token exposed")
			}
		})
	}
}

// TestIntegrationsRejectInvalidSandboxStateInOrder proves every delivered workflow reports the earliest sandbox failure before reading token input at the host CLI and fake Podman seam.
func TestIntegrationsRejectInvalidSandboxStateInOrder(t *testing.T) {
	commands := [][]string{
		{"config", "agent01", "git", "identity"},
		{"config", "agent01", "git", "credentials"},
		{"login", "agent01", "github"},
		{"login", "agent01", "azdo", "--organization", "https://dev.azure.com/contoso"},
	}
	for _, command := range commands {
		for _, tc := range []struct {
			name, message string
			configure     func(*fixture)
		}{
			{"unknown", "does not exist", func(f *fixture) { f.responses["container exists "+shellContainer] = response{Exit: 1} }},
			{"volumes-only", "up agent01", func(f *fixture) {
				f.responses["container exists "+shellContainer] = response{Exit: 1}
				f.responses["volume exists "+sandboxVolumes[1]] = response{}
			}},
			{"foreign-volume-only", "owner conflict", func(f *fixture) {
				f.responses["container exists "+shellContainer] = response{Exit: 1}
				f.responses["volume exists "+sandboxVolumes[1]] = response{}
				f.responses["volume inspect "+sandboxVolumes[1]] = response{Output: `[{"Labels":{}}]`}
			}},
			{"container-owner-before-backup", "owner conflict", func(f *fixture) {
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{}`, "false")}
				f.responses["container exists "+sandboxBackup] = response{}
			}},
			{"volume-owner-before-backup", "owner conflict", func(f *fixture) {
				f.responses["volume exists "+sandboxVolumes[1]] = response{}
				f.responses["volume inspect "+sandboxVolumes[1]] = response{Output: `[{"Labels":{}}]`}
				f.responses["container exists "+sandboxBackup] = response{}
			}},
			{"backup-before-stopped", "update agent01", func(f *fixture) {
				f.responses["container exists "+sandboxBackup] = response{}
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
			}},
			{"stopped-before-manager", "start agent01", func(f *fixture) {
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
			}},
			{"manager-before-input", "manager does not answer", func(f *fixture) { f.responses[integrationProbe] = response{Exit: 125} }},
		} {
			t.Run(strings.Join(command, " ")+"/"+tc.name, func(t *testing.T) {
				f := sandboxStateFixture(t)
				tc.configure(f)
				f.host.Stdin = unreadableToken{t}
				f.host.IsInputTerminal = func(io.Reader) bool { t.Error("input checked before sandbox failure"); return false }
				status, output, calls := f.run(t, append([]string{"integrations"}, command...)...)
				if status != 1 || !strings.Contains(output, tc.message) {
					t.Fatalf("status=%d output=%s", status, output)
				}
				for _, call := range calls {
					if strings.Contains(strings.Join(call, " "), "--user=1000:1000") {
						t.Fatal("workflow ran after guard failure")
					}
				}
			})
		}
	}
}

type unreadableToken struct{ t *testing.T }

func (r unreadableToken) Read([]byte) (int, error) {
	r.t.Error("token read before guard")
	return 0, io.EOF
}

// TestAzureDevOpsRejectsEmptyToken proves empty stdin refuses before any credential workflow at the host CLI seam.
func TestAzureDevOpsRejectsEmptyToken(t *testing.T) {
	for _, input := range []string{"", "\n", "  \r\n"} {
		f := azureIntegrationFixture(t)
		f.host.Stdin = strings.NewReader(input)
		f.responses[integrationProbe] = response{}
		status, output, calls := f.run(t, "integrations", "login", "agent01", "azdo", "--organization", "https://dev.azure.com/contoso")
		if status != 1 || !strings.Contains(output, "token is required") || len(calls) != len(sandboxQueryCalls())+1 {
			t.Fatalf("status=%d output=%s calls=%q", status, output, calls)
		}
	}
}

// TestAzureDevOpsUsageFailsBeforePodman proves invalid organizations, unexpected workflow names, and token options fail at the host CLI boundary before sandbox inspection.
func TestAzureDevOpsUsageFailsBeforePodman(t *testing.T) {
	for _, suffix := range [][]string{
		{}, {"extra", "--organization", "https://dev.azure.com/contoso"},
		{"--organization", "http://dev.azure.com/contoso"},
		{"--organization", "https://user:secret@dev.azure.com/contoso"},
		{"--organization", "https://dev.azure.com/contoso%0apassword=oops"},
		{"--organization", "https://dev.azure.com"},
		{"--organization", "https://dev.azure.com/contoso", "--token", "pat-sentinel"},
	} {
		f := sandboxStateFixture(t)
		status, _, calls := f.run(t, append([]string{"integrations", "login", "agent01", "azdo"}, suffix...)...)
		if status != 2 || len(calls) != 0 {
			t.Fatalf("status=%d calls=%q", status, calls)
		}
	}
}

// TestAzureDevOpsRejectsWorkflowWithDeliveredGrammar proves the host explains Azure DevOps accepts no workflow without falsely describing it as undelivered at the CLI seam.
func TestAzureDevOpsRejectsWorkflowWithDeliveredGrammar(t *testing.T) {
	f := sandboxStateFixture(t)
	status, output, calls := f.run(t, "integrations", "login", "agent01", "azdo", "bogusflow", "--organization", "https://dev.azure.com/contoso")
	if status != 2 || len(calls) != 0 || !strings.Contains(output, "azdo requires --organization URL and no workflow") || strings.Contains(output, "no valid names are available yet") {
		t.Fatalf("status=%d output=%s calls=%q", status, output, calls)
	}
}

// TestAzureDevOpsMissingToolchainDoesNotReadToken proves missing Azure prerequisites print the additive update command before inspecting or reading PAT input through RunWithHost and injected input/process boundaries.
func TestAzureDevOpsMissingToolchainDoesNotReadToken(t *testing.T) {
	for _, tc := range []struct{ recorded, command string }{
		{"none", "update agent01 --with azure"},
		{"dotnet,native", "update agent01 --with azure,dotnet,native"},
	} {
		t.Run(tc.recorded, func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"`+tc.recorded+`"}`, "true")}
			f.responses[integrationProbe] = response{}
			f.host.Stdin = unreadableToken{t}
			f.host.IsInputTerminal = func(io.Reader) bool { t.Error("token input inspected before missing toolchain refusal"); return false }
			status, output, calls := f.run(t, "integrations", "login", "agent01", "azdo", "--organization", "https://dev.azure.com/contoso")
			want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
			if status != 1 || !strings.Contains(output, "azure toolchain") || !strings.HasSuffix(strings.TrimSpace(output), tc.command) || !reflect.DeepEqual(calls, want) {
				t.Fatalf("status=%d output=%s calls=%v; want missing toolchain with %q and queries %v", status, output, calls, tc.command, want)
			}
		})
	}
}
