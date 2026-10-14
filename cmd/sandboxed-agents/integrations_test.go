package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestGitIntegrationUsagePrecedesPodman proves invalid integration requests list delivered choices without external calls at the executable CLI seam.
func TestGitIntegrationUsagePrecedesPodman(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		messages []string
	}{
		{[]string{"login", "agent01", "git"}, []string{"identity", "credentials", "config"}},
		{[]string{"config", "agent01", "git"}, []string{"identity", "credentials"}},
		{[]string{"config", "agent01", "unknown"}, []string{"unknown integration", "valid names: git"}},
		{[]string{"config", "agent01", "git", "unknown"}, []string{"unknown", "identity", "credentials"}},
		{[]string{"login", "agent01", "unknown"}, []string{"unknown integration", "no valid names are available yet"}},
		{[]string{"config", "agent01", "git", "credentials", "--name", "N"}, []string{"takes no options"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, append([]string{"integrations"}, tc.args...)...)
			command.Env = f.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != 2 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			for _, message := range tc.messages {
				if !strings.Contains(string(output), message) {
					t.Errorf("output=%s missing %q", output, message)
				}
			}
			assertNoExternalCalls(t, f)
		})
	}
}

// TestGitConfigExecutableWithoutTerminal proves both workflows and refusal paths obey ordered checks and exact Podman arguments with no host data at the built CLI seam.
func TestGitConfigExecutableWithoutTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, workflow    string
		options           []string
		running           bool
		probeExit, status int
		messages          []string
	}{
		{"credentials", "credentials", nil, true, 0, 0, nil},
		{"identity", "identity", []string{"--name", "A Person", "--email", "a@example.com"}, true, 0, 0, nil},
		{"missing-email", "identity", []string{"--name", "N"}, true, 0, 1, []string{"terminal"}},
		{"missing-name", "identity", []string{"--email", "E"}, true, 0, 1, []string{"terminal"}},
		{"stopped-identity", "identity", nil, false, 0, 1, []string{"start agent01"}},
		{"stopped-credentials", "credentials", nil, false, 0, 1, []string{"start agent01"}},
		{"manager-identity", "identity", nil, true, 125, 1, []string{"manager does not answer", "check agent01", "restart agent01"}},
		{"manager-credentials", "credentials", nil, true, 125, 1, []string{"manager does not answer", "check agent01", "restart agent01"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := testutil.Install(t, fakeExecutable)
			container := "sandboxed-agents.default.agent01"
			managerPath := "/usr/local/bin/sandboxed-agents-manager"
			prefix := ""
			var want [][]string
			responses := map[string]lifecycleResponse{}
			if runtime.GOOS == "windows" {
				prefix = "--connection chosen "
				responses["machine list --format json"] = lifecycleResponse{Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`}
				responses["machine inspect chosen"] = lifecycleResponse{Output: `[{"Name":"chosen","State":"running","Rootful":false}]`}
				want = [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
			}
			addCall := func(args []string) {
				if prefix != "" {
					args = append([]string{"--connection", "chosen"}, args...)
				}
				want = append(want, args)
			}
			responses[prefix+"container exists "+container] = lifecycleResponse{}
			responses[prefix+"container inspect "+container] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":` + strconv.FormatBool(tc.running) + `}}]`}
			addCall([]string{"container", "exists", container})
			addCall([]string{"container", "inspect", container})
			for _, suffix := range []string{".workspace", ".home", ".ssh"} {
				responses[prefix+"volume exists "+container+suffix] = lifecycleResponse{Exit: 1}
				addCall([]string{"volume", "exists", container + suffix})
			}
			backup := "sandboxed-agents-backup.default.agent01"
			responses[prefix+"container exists "+backup] = lifecycleResponse{Exit: 1}
			addCall([]string{"container", "exists", backup})
			probe := []string{"exec", "--user=0:0", container, managerPath, "sessions"}
			responses[prefix+strings.Join(probe, " ")] = lifecycleResponse{Exit: tc.probeExit}
			if tc.running {
				addCall(probe)
			}
			workflow := append([]string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", container, managerPath, "integrations", "config", "git", tc.workflow}, tc.options...)
			responses[prefix+strings.Join(workflow, " ")] = lifecycleResponse{}
			if tc.status == 0 {
				addCall(workflow)
			}
			data, err := json.Marshal(responses)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(executable, append([]string{"integrations", "config", "agent01", "git", tc.workflow}, tc.options...)...)
			command.Env = testutil.Env(f.Env(os.Environ()), map[string]string{"TEST_PODMAN_RESPONSES": string(data), "GITHUB_TOKEN": "host-token-sentinel", "GIT_CONFIG_GLOBAL": "/host/config", "GIT_ASKPASS": "/host/askpass"})
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d output=%s want=%d", status, output, tc.status)
			}
			for _, message := range tc.messages {
				if !strings.Contains(string(output), message) {
					t.Errorf("output=%s missing=%q", output, message)
				}
			}
			if calls := testutil.Invocations(t, f.PodmanLog); !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
			if calls := testutil.Invocations(t, f.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected SSH calls=%v", calls)
			}
		})
	}
}
