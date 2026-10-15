package cli_test

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestAzureBrowserRequiresSSHSetup proves missing host setup refuses before any login or terminal check through the host CLI seam.
func TestAzureBrowserRequiresSSHSetup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := sandboxStateFixture(t)
	f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}`, "true")}
	f.responses[integrationProbe] = response{}
	f.host.IsInputTerminal = func(io.Reader) bool { t.Error("terminal checked before SSH setup"); return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
	if status != 1 || !strings.Contains(output, "ssh-config agent01 --install") || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
	}
}

// TestAzureLoginRequiresToolchainBeforeTerminal proves Azure names the complete additive update command before checking terminal or launching login through the host CLI seam.
func TestAzureLoginRequiresToolchainBeforeTerminal(t *testing.T) {
	for _, tc := range []struct{ set, want string }{{"none", "update agent01 --with azure"}, {"dotnet,native", "update agent01 --with azure,dotnet,native"}} {
		f := sandboxStateFixture(t)
		f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"`+tc.set+`"}`, "true")}
		f.responses[integrationProbe] = response{}
		f.host.IsInputTerminal = func(io.Reader) bool { t.Error("terminal checked before toolchain"); return false }
		status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", "device-code")
		if status != 1 || !strings.Contains(output, tc.want) || !reflect.DeepEqual(calls, append(sandboxQueryCalls(), strings.Fields(integrationProbe))) {
			t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
		}
	}
}

// TestAzureLoginDispatchesAgentWithTerminal proves both Azure workflows preserve terminal streams, identity, and failure normalization through the host CLI seam.
func TestAzureLoginDispatchesAgentWithTerminal(t *testing.T) {
	for _, workflow := range []string{"device-code", "browser"} {
		for _, code := range []int{0, 23} {
			t.Run(workflow+strconv.Itoa(code), func(t *testing.T) {
				f := sandboxStateFixture(t)
				state := t.TempDir()
				t.Setenv("XDG_STATE_HOME", state)
				t.Setenv("LOCALAPPDATA", state)
				t.Setenv("AZURE_CONFIG_DIR", "/host/azure")
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}`, "true")}
				f.responses[integrationProbe] = response{}
				f.host.IsInputTerminal = func(io.Reader) bool { return true }
				f.host.IsOutputTerminal = func(io.Writer) bool { return true }
				f.host.Stdin = strings.NewReader("azure-terminal-input\n")
				execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -it " + shellContainer + " " + integrationManager + " integrations login azure " + workflow)
				if workflow == "browser" {
					hash := sha256.Sum256([]byte("agent01"))
					dir := filepath.Join(state, "sandboxed-agents", "group-default", "ssh", fmt.Sprintf("sandbox-%x", hash))
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("Host agent01\n"), 0600); err != nil {
						t.Fatal(err)
					}
					execution = append(execution, "--ssh-host", "agent01")
				}
				f.responses[strings.Join(execution, " ")] = response{Exit: code, EchoStdin: true}
				status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", workflow)
				wantStatus := 0
				if code != 0 {
					wantStatus = 1
				}
				if status != wantStatus || !strings.Contains(output, "azure-terminal-input") || !reflect.DeepEqual(calls, append(sandboxQueryCalls(), strings.Fields(integrationProbe), execution)) {
					t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
				}
			})
		}
	}
}
