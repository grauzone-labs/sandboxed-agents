package main_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// integrationRefusalHarness allows only the ordered state and manager queries preceding a login refusal.
func integrationRefusalHarness(t *testing.T, toolchains string) (testutil.Fakes, [][]string) {
	t.Helper()
	container := "sandboxed-agents.default.agent01"
	calls := [][]string{
		{"container", "exists", container},
		{"container", "inspect", container},
		{"volume", "exists", container + ".workspace"},
		{"volume", "exists", container + ".home"},
		{"volume", "exists", container + ".ssh"},
		{"container", "exists", "sandboxed-agents-backup.default.agent01"},
		{"exec", "--user=0:0", container, "/usr/local/bin/sandboxed-agents-manager", "sessions"},
	}
	responses := map[string]lifecycleResponse{}
	for _, call := range calls {
		response := lifecycleResponse{}
		if call[0] == "volume" || strings.Contains(call[len(call)-1], "sandboxed-agents-backup") {
			response.Exit = 1
		}
		responses[strings.Join(call, " ")] = response
	}
	responses["container inspect "+container] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"` + toolchains + `"}},"State":{"Running":true}}]`}
	f := agentExecutableHarness(t, responses)
	if runtime.GOOS == "windows" {
		want := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
		for _, call := range calls {
			want = append(want, append([]string{"--connection", "chosen"}, call...))
		}
		calls = want
	}
	return f, calls
}

// TestAzureToolchainRefusalsExecutable proves Azure and Azure DevOps print complete additive toolchain updates without none or mutation through the built CLI and fake processes.
func TestAzureToolchainRefusalsExecutable(t *testing.T) {
	for _, login := range [][]string{
		{"azure", "device-code"},
		{"azure", "browser"},
		{"azdo", "--organization", "https://dev.azure.com/contoso"},
	} {
		for _, tc := range []struct{ recorded, command string }{
			{"none", "update agent01 --with azure"},
			{"dotnet,native", "update agent01 --with azure,dotnet,native"},
		} {
			t.Run(strings.Join(login, " ")+"/"+tc.recorded, func(t *testing.T) {
				f, want := integrationRefusalHarness(t, tc.recorded)
				status, output := runAgentExecutable(t, append([]string{"integrations", "login", "agent01"}, login...)...)
				if status != 1 || !strings.Contains(output, "azure toolchain") {
					t.Fatalf("status=%d output=%s; want missing azure toolchain", status, output)
				}
				_, command, found := strings.Cut(output, "run ")
				if !found || strings.TrimSpace(command) != tc.command {
					t.Errorf("output=%s; want complete command %q", output, tc.command)
				}
				if got := testutil.Invocations(t, f.PodmanLog); !reflect.DeepEqual(got, want) {
					t.Errorf("calls=%v; want queries only %v", got, want)
				}
				if got := testutil.Invocations(t, f.SSHLog); len(got) != 0 {
					t.Errorf("unexpected SSH calls=%v", got)
				}
			})
		}
	}
}

// TestIntegrationLoginsRefuseWithoutTerminalExecutable proves GitHub and both Azure logins refuse without starting a workflow after all prerequisites through the built CLI and fake processes.
func TestIntegrationLoginsRefuseWithoutTerminalExecutable(t *testing.T) {
	for _, login := range [][]string{{"github"}, {"azure", "device-code"}, {"azure", "browser"}} {
		t.Run(strings.Join(login, " "), func(t *testing.T) {
			f, want := integrationRefusalHarness(t, "azure")
			var entry string
			const setup = "Host agent01\n  HostName 127.0.0.1\n"
			if len(login) == 2 && login[1] == "browser" {
				// Supply the installed host entry so the earlier SSH prerequisite passes.
				hash := sha256.Sum256([]byte("agent01"))
				entry = filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "group-default", "ssh", fmt.Sprintf("sandbox-%x", hash), "entry")
				if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(entry, []byte(setup), 0600); err != nil {
					t.Fatal(err)
				}
			}
			status, output := runAgentExecutable(t, append([]string{"integrations", "login", "agent01"}, login...)...)
			if status != 1 || !strings.Contains(output, login[0]+" login needs an interactive terminal") {
				t.Fatalf("status=%d output=%s; want missing interactive terminal", status, output)
			}
			if got := testutil.Invocations(t, f.PodmanLog); !reflect.DeepEqual(got, want) {
				t.Errorf("calls=%v; want queries only %v", got, want)
			}
			if got := testutil.Invocations(t, f.SSHLog); len(got) != 0 {
				t.Errorf("unexpected SSH calls=%v", got)
			}
			if entry != "" {
				if data, err := os.ReadFile(entry); err != nil || string(data) != setup {
					t.Errorf("SSH setup changed: data=%q error=%v", data, err)
				}
			}
		})
	}
}
