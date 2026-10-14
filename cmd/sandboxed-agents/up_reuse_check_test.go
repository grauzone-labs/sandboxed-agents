package main_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestUpExistingMatchesConfigurationBeforeStarting proves absent and matching options start a sandbox while changed resources and workspace refuse through the CLI and fake Podman seams.
func TestUpExistingMatchesConfigurationBeforeStarting(t *testing.T) {
	for _, options := range [][]string{nil, {"--memory", "9g", "--cpus", "2.5", "--pids-limit", "3000", "--shm-size", "2g"}, {"--memory", "8g"}, {"--cpus", "4"}, {"--pids-limit", "2048"}, {"--shm-size", "1g"}, {"WORKSPACE"}, {"OTHER_WORKSPACE"}} {
		t.Run(strings.Join(options, " "), func(t *testing.T) {
			workspace := t.TempDir()
			responses := healthyCheckResponses(false)
			mounts, err := json.Marshal([]map[string]string{{"Type": "bind", "Source": workspace, "Destination": "/workspace"}})
			if err != nil {
				t.Fatal(err)
			}
			record := responses["container inspect sandboxed-agents.default.demo"]
			record.Output = strings.Replace(record.Output, `"State":`, `"Mounts":`+string(mounts)+`,"State":`, 1)
			responses["container inspect sandboxed-agents.default.demo"] = record
			responses["--version"] = lifecycleResponse{Output: "podman version 4.4.0"}
			responses["info --format json"] = lifecycleResponse{Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`}
			fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
			scriptSetupResponses(t, responses, "linux")
			host := lifecycleLinuxHost(t, fakes)
			host.PortAvailable = func(uint16) error { return nil }
			args := []string{"up", "demo"}
			matching := len(options) == 0 || len(options) > 2 || options[0] == "WORKSPACE"
			if len(options) == 1 {
				if options[0] == "OTHER_WORKSPACE" {
					workspace = t.TempDir()
				}
				args = append(args, workspace)
			} else {
				args = append(args, options...)
			}
			before := setupFiles(t)
			status, output := runWithHost(host, args...)
			if (status == 0) != matching {
				t.Fatalf("reuse=%d %s", status, output)
			}
			if !matching {
				want := "workspace"
				if len(options) == 2 {
					want = options[0]
				}
				for _, message := range []string{want, "differs", "remove demo", "up demo"} {
					if !strings.Contains(output, message) {
						t.Fatalf("missing %q: %s", message, output)
					}
				}
			}
			mutations := lifecycleMutations(testutil.Invocations(t, fakes.PodmanLog))
			if matching && !reflect.DeepEqual(mutations, [][]string{{"start", "sandboxed-agents.default.demo"}}) || !matching && len(mutations) != 0 {
				t.Fatalf("reuse mutations=%v", mutations)
			}
			if !reflect.DeepEqual(before, setupFiles(t)) {
				t.Fatal("reuse changed SSH files")
			}
		})
	}
}

// TestUpResourceOverridesAreReportedByCheck proves resource overrides reach container creation and later diagnostics through the CLI and fake Podman seams.
func TestUpResourceOverridesAreReportedByCheck(t *testing.T) {
	responses := newSandboxResponses()
	responses["container inspect sandboxed-agents.default.demo"] = healthyCheckResponses(true)["container inspect sandboxed-agents.default.demo"]
	fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
	scriptSetupResponses(t, responses, "linux")
	host := lifecycleLinuxHost(t, fakes)
	host.PortAvailable = func(uint16) error { return nil }
	status, output := runWithHost(host, "up", "demo", "--memory", "9g", "--cpus", "2.5", "--pids-limit", "3000", "--shm-size", "2g")
	if status != 0 {
		t.Fatalf("up=%d %s", status, output)
	}
	created := false
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) == 0 || call[0] != "create" {
			continue
		}
		created = true
		joined := strings.Join(call, " ")
		for _, option := range []string{"--memory 9663676416", "--cpus 2.5", "--pids-limit 3000", "--shm-size 2147483648"} {
			if !strings.Contains(joined, option) {
				t.Fatalf("missing %q: %v", option, call)
			}
		}
	}
	if !created {
		t.Fatal("up never created the sandbox")
	}
	scriptSetupResponses(t, healthyCheckResponses(true), "linux")
	before := len(testutil.Invocations(t, fakes.PodmanLog))
	var stdout, stderr strings.Builder
	status = cli.RunWithHost([]string{"check", "demo"}, &stdout, &stderr, host)
	if status != 0 {
		t.Fatalf("check=%d %s %s", status, stdout.String(), stderr.String())
	}
	for _, report := range []string{"memory 9663676416", "cpus 2.5", "pids-limit 3000", "shm-size 2147483648"} {
		if !strings.Contains(stdout.String(), report) {
			t.Fatalf("missing %q: %s", report, stdout.String())
		}
	}
	assertCheckQueriesOnly(t, fakes, before)
}
