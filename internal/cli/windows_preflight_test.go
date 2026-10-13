package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func windowsHost(t *testing.T) *fixture {
	t.Helper()
	f := linuxHost(t)
	f.host.Platform = "windows"
	f.host.WindowsBuild = func() (uint32, error) { return 22621, nil }
	f.host.WindowsWorkstation = func() (bool, error) { return true, nil }
	f.responses = map[string]response{
		"--connection chosen " + portInventory:      {Output: "[]"},
		"--version":                                 {Output: "podman version 4.7.0"},
		"machine list --format json":                {Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`},
		"machine inspect chosen":                    {Output: `[{"Name":"chosen","State":"running","Rootful":false}]`},
		"--connection chosen version --format json": {Output: `{"Client":{"Version":"4.7.0"},"Server":{"Version":"4.7.0"}}`},
		"--connection chosen info --format json":    {Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`},
		"machine ssh chosen id -u":                  {Output: "1000\n"},
		"machine ssh chosen cat /proc/self/cgroup":  {Output: "0::/user.slice/user-1000.slice/session-1.scope\n"},
		windowsControllerRead:                       {Output: "cpu memory pids"},
	}
	return f
}

// windowsControllerRead names the read-only delegated controller query.
const windowsControllerRead = "machine ssh chosen cat /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"

// TestWindowsPreflightMissingPrerequisites proves Windows host requirements have distinct diagnostics through RunWithHost and real fake Podman processes on any test platform.
func TestWindowsPreflightMissingPrerequisites(t *testing.T) {
	cases := []struct {
		name, message string
		breakHost     func(*testing.T, *fixture)
	}{
		{"client-version", "Podman >= 4.7.0", func(t *testing.T, f *fixture) { f.responses["--version"] = response{Output: "podman version 4.6.2"} }},
		{"machine-missing", "a Podman machine is required", func(t *testing.T, f *fixture) { f.responses["machine list --format json"] = response{Output: "[]"} }},
		{"machine-ambiguous", "select one default Podman machine", func(t *testing.T, f *fixture) {
			f.responses["machine list --format json"] = response{Output: `[{"Name":"one"},{"Name":"two"}]`}
		}},
		{"machine-name", "invalid Podman machine name", func(t *testing.T, f *fixture) {
			f.responses["machine list --format json"] = response{Output: `[{"Name":"-bad","VMType":"wsl"}]`}
		}},
		{"machine-stopped", "a running Podman machine is required", func(t *testing.T, f *fixture) {
			f.responses["machine inspect chosen"] = response{Output: `[{"Name":"chosen","State":"stopped","Rootful":false}]`}
		}},
		{"machine-provider", "a WSL2 Podman machine is required", func(t *testing.T, f *fixture) {
			f.responses["machine list --format json"] = response{Output: `[{"Name":"chosen","VMType":"qemu","Default":true}]`}
		}},
		{"machine-rootful", "a rootless Podman machine is required", func(t *testing.T, f *fixture) {
			f.responses["machine inspect chosen"] = response{Output: `[{"Name":"chosen","State":"running","Rootful":true}]`}
		}},
		{"engine-version", "Podman machine >= 4.7.0", func(t *testing.T, f *fixture) {
			f.responses["--connection chosen version --format json"] = response{Output: `{"Server":{"Version":"4.6.2"}}`}
		}},
		{"machine-cgroup-v2", "cgroups v2 is required", func(t *testing.T, f *fixture) {
			f.responses["--connection chosen info --format json"] = response{Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v1"}}`}
		}},
		{"machine-cpu", "delegated cgroup cpu controller", func(t *testing.T, f *fixture) {
			f.responses[windowsControllerRead] = response{Output: "memory pids"}
		}},
		{"machine-memory", "delegated cgroup memory controller", func(t *testing.T, f *fixture) {
			f.responses[windowsControllerRead] = response{Output: "cpu pids"}
		}},
		{"machine-pids", "delegated cgroup pids controller", func(t *testing.T, f *fixture) {
			f.responses[windowsControllerRead] = response{Output: "cpu memory"}
		}},
		{"podman", "podman is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "podman")) }},
		{"ssh", "ssh is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh")) }},
		{"ssh-keygen", "ssh-keygen is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh-keygen")) }},
		{"architecture", "Windows 11 x64 is required", func(t *testing.T, f *fixture) { f.host.Architecture = "arm64" }},
		{"machine-rootful-unknown", "a rootless Podman machine is required", func(t *testing.T, f *fixture) {
			f.responses["machine inspect chosen"] = response{Output: `[{"Name":"chosen","State":"running"}]`}
		}},
		{"engine-rootless", "rootless Podman is required", func(t *testing.T, f *fixture) {
			f.responses["--connection chosen info --format json"] = response{Output: `{"host":{"security":{"rootless":false},"cgroupVersion":"v2"}}`}
		}},
		{"windows-server", "Windows 11 x64 is required", func(t *testing.T, f *fixture) { f.host.WindowsWorkstation = func() (bool, error) { return false, nil } }},
		{"windows11", "Windows 11 x64 is required", func(t *testing.T, f *fixture) { f.host.WindowsBuild = func() (uint32, error) { return 19045, nil } }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range [][]string{{"check"}, {"build"}, {"up", "demo"}} {
				t.Run(strings.Join(args, "-"), func(t *testing.T) {
					f := windowsHost(t)
					tc.breakHost(t, f)
					status, output, calls := f.run(t, args...)
					if status != 1 || !strings.Contains(output, tc.message) {
						t.Fatalf("status=%d output=%q; want %q", status, output, tc.message)
					}
					noMutation(t, calls)
				})
			}
		})
	}
}

// TestWindowsTargetSelectionAndEnvironment proves default or sole machines bind remote queries and Podman loses ambient remote variables through RunWithHost and the fake process seam.
func TestWindowsTargetSelectionAndEnvironment(t *testing.T) {
	for _, list := range []string{`[{"Name":"chosen","VMType":"wsl"}]`, `[{"Name":"other","VMType":"qemu"},{"Name":"chosen","Default":true,"VMType":"wsl"}]`} {
		t.Run(list, func(t *testing.T) {
			f := windowsHost(t)
			f.responses["machine list --format json"] = response{Output: list}
			for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY"} {
				t.Setenv(key, "ambient")
			}
			envLog := filepath.Join(t.TempDir(), "environment.json")
			t.Setenv("TEST_PODMAN_ENV_LOG", envLog)
			status, output, calls := f.run(t, "check")
			if status != 0 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			noMutation(t, calls)
			for _, call := range calls {
				if len(call) > 0 && (call[0] == "version" || call[0] == "info") {
					t.Fatalf("unbound query: %v", call)
				}
			}
			data, err := os.ReadFile(envLog)
			if err != nil {
				t.Fatal(err)
			}
			var environment []string
			if err := json.Unmarshal(data, &environment); err != nil {
				t.Fatal(err)
			}
			for _, entry := range environment {
				if strings.HasPrefix(strings.ToUpper(entry), "CONTAINER_") {
					t.Fatalf("ambient remote variable leaked: %q", entry)
				}
			}
		})
	}
}

// TestWindowsQueriesFailClosed proves missing server version, unreadable machine queries, and invalid machine selection refuse work at the exported CLI seam.
func TestWindowsQueriesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     response
	}{
		{"list-failed", "machine list --format json", response{Exit: 125}},
		{"list-malformed", "machine list --format json", response{Output: "{"}},
		{"multiple-defaults", "machine list --format json", response{Output: `[{"Name":"one","Default":true},{"Name":"two","Default":true}]`}},
		{"inspect-failed", "machine inspect chosen", response{Exit: 125}},
		{"inspect-malformed", "machine inspect chosen", response{Output: "{"}},
		{"server-missing", "--connection chosen version --format json", response{Output: `{"Client":{"Version":"9.0.0"}}`}},
		{"server-failed", "--connection chosen version --format json", response{Exit: 125}},
		{"controllers-failed", windowsControllerRead, response{Exit: 125}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := windowsHost(t)
			f.responses[tc.key] = tc.value
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "step 2:") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsControllerQueriesUseSimpleMachineCommands proves delegation discovery uses simple machine-named read-only argv through RunWithHost and fake Podman.
func TestWindowsControllerQueriesUseSimpleMachineCommands(t *testing.T) {
	f := windowsHost(t)
	status, output, calls := f.run(t, "check")
	if status != 0 {
		t.Fatalf("status=%d output=%q", status, output)
	}
	queries := 0
	for _, call := range calls {
		if len(call) > 2 && call[0] == "machine" && call[1] == "ssh" {
			queries++
			if len(call) != 5 || (call[3] != "id" && call[3] != "cat") {
				t.Fatalf("unexpected delegation argv: %v", call)
			}
		}
	}
	if queries != 3 {
		t.Fatalf("queries=%d calls=%v", queries, calls)
	}
	noMutation(t, calls)
}

// TestWindowsPreflightReportsMissingPodmanDependencies proves a missing client reports every dependent Windows prerequisite as unverifiable without querying or starting a machine through RunWithHost.
func TestWindowsPreflightReportsMissingPodmanDependencies(t *testing.T) {
	f := windowsHost(t)
	if err := os.Remove(fakePath(f, "podman")); err != nil {
		t.Fatal(err)
	}
	status, output, calls := f.run(t, "check")
	for _, message := range []string{"podman is required on PATH", "Podman >= 4.7.0 is required", "a running Podman machine is required", "a WSL2 Podman machine is required", "a rootless Podman machine is required", "Podman machine >= 4.7.0 is required", "rootless Podman is required", "cgroups v2 is required", "delegated cgroup cpu controller is required", "delegated cgroup memory controller is required", "delegated cgroup pids controller is required"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 || !strings.Contains(output, "cannot verify") || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
}

// TestWindowsPreflightReportsOldClientAndMissingMachine proves an old client and absent machine are reported together with dependent engine checks through RunWithHost and fake Podman.
func TestWindowsPreflightReportsOldClientAndMissingMachine(t *testing.T) {
	f := windowsHost(t)
	f.responses["--version"] = response{Output: "podman version 4.6.2"}
	f.responses["machine list --format json"] = response{Output: "[]"}
	status, output, calls := f.run(t, "check")
	for _, message := range []string{"Podman >= 4.7.0 is required", "a Podman machine is required", "Podman machine >= 4.7.0 is required (cannot verify:", "cgroups v2 is required (cannot verify:"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestWindowsPreflightAggregatesStoppedProviderAndRootfulFailures proves independent machine state, provider, and identity failures are all reported without engine queries through RunWithHost.
func TestWindowsPreflightAggregatesStoppedProviderAndRootfulFailures(t *testing.T) {
	f := windowsHost(t)
	f.responses["machine list --format json"] = response{Output: `[{"Name":"chosen","Default":true,"VMType":"qemu"}]`}
	f.responses["machine inspect chosen"] = response{Output: `[{"Name":"chosen","State":"stopped","Rootful":true}]`}
	status, output, calls := f.run(t, "check")
	for _, message := range []string{"a running Podman machine is required", "a WSL2 Podman machine is required", "a rootless Podman machine is required", "delegated cgroup cpu controller is required (cannot verify:", "delegated cgroup memory controller is required (cannot verify:", "delegated cgroup pids controller is required (cannot verify:"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 || len(calls) != 3 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}

// TestWindowsUnselectedMachineReportsUnverifiedRequirements proves missing or unsafe machine selections report dependent machine facts without inspecting unsafe names through RunWithHost.
func TestWindowsUnselectedMachineReportsUnverifiedRequirements(t *testing.T) {
	for _, list := range []string{`[]`, `[{"Name":"-unsafe","VMType":"wsl"}]`} {
		t.Run(list, func(t *testing.T) {
			f := windowsHost(t)
			f.responses["machine list --format json"] = response{Output: list}
			status, output, calls := f.run(t, "check")
			for _, message := range []string{"a running Podman machine is required (cannot verify:", "a WSL2 Podman machine is required (cannot verify:", "a rootless Podman machine is required (cannot verify:"} {
				if !strings.Contains(output, message) {
					t.Errorf("missing %q: %s", message, output)
				}
			}
			if status != 1 || len(calls) != 2 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsFailedInfoReportsUnverifiedPrerequisites proves an unreadable engine info query reports rootless and cgroup version checks separately through RunWithHost.
func TestWindowsFailedInfoReportsUnverifiedPrerequisites(t *testing.T) {
	f := windowsHost(t)
	f.responses["--connection chosen info --format json"] = response{Exit: 125}
	status, output, calls := f.run(t, "check")
	for _, message := range []string{"rootless Podman is required (cannot verify:", "cgroups v2 is required (cannot verify:"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}
