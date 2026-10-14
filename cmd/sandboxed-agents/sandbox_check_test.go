package main_test

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// healthyCheckResponses scripts an owned sandbox with all volumes and explicit resource limits.
func healthyCheckResponses(running bool) map[string]lifecycleResponse {
	responses := setupResponses(running)
	record := responses["container inspect sandboxed-agents.default.demo"]
	record.Output = strings.Replace(record.Output, `"State":`, `"HostConfig":{"Memory":9663676416,"CpuQuota":250000,"CpuPeriod":100000,"PidsLimit":3000,"ShmSize":2147483648},"State":`, 1)
	responses["container inspect sandboxed-agents.default.demo"] = record
	for _, kind := range []string{"workspace", "home", "ssh"} {
		name := "sandboxed-agents.default.demo." + kind
		responses["volume exists "+name] = lifecycleResponse{}
		responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"default"}}]`}
	}
	return responses
}

// TestCheckObjectOwnerProblemsAreNamed proves missing and foreign owners are diagnosed on every sandbox object through the built CLI and fake Podman seam.
func TestCheckObjectOwnerProblemsAreNamed(t *testing.T) {
	for _, object := range []string{"container", "workspace", "home", "ssh", "backup"} {
		for _, owner := range []string{"", "other"} {
			t.Run(object+"/owner="+owner, func(t *testing.T) {
				responses := healthyCheckResponses(false)
				name := "sandboxed-agents.default.demo"
				if object == "container" {
					record := responses["container inspect "+name]
					record.Output = strings.ReplaceAll(record.Output, `"default"`, `"`+owner+`"`)
					responses["container inspect "+name] = record
				} else if object == "backup" {
					name = "sandboxed-agents-backup.default.demo"
					responses["container exists "+name] = lifecycleResponse{}
					responses["container inspect "+name] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}}]`}
				} else {
					name += "." + object
					responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				}
				fakes := sshSetupHarness(t, responses)
				status, stdout, stderr := sshSetupRun(t, "check", "demo")
				if status == 0 || !strings.Contains(stderr, "owner conflict") || !strings.Contains(stderr, name) || !strings.Contains(stderr, "Podman") {
					t.Fatalf("owner diagnostic=%d %q %q", status, stdout, stderr)
				}
				assertCheckQueriesOnly(t, fakes, 0)
			})
		}
	}
}

// TestCheckVolumesOnlyAndUnknownSandboxStates proves complete and partial kept-volume sets are healthy while missing sandboxes and leftover backups fail through the built CLI and fake Podman seam.
func TestCheckVolumesOnlyAndUnknownSandboxStates(t *testing.T) {
	for _, state := range []string{"all volumes", "workspace", "home", "ssh", "unknown", "backup"} {
		t.Run(state, func(t *testing.T) {
			responses := healthyCheckResponses(false)
			responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
			for _, kind := range []string{"workspace", "home", "ssh"} {
				if state != "all volumes" && state != kind {
					responses["volume exists sandboxed-agents.default.demo."+kind] = lifecycleResponse{Exit: 1}
				}
			}
			if state == "backup" {
				responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
				responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
			}
			fakes := sshSetupHarness(t, responses)
			status, stdout, stderr := sshSetupRun(t, "check", "demo")
			if (status != 0) != (state == "unknown" || state == "backup") {
				t.Fatalf("check=%d %q %q", status, stdout, stderr)
			}
			if state == "unknown" && !strings.Contains(stderr, "does not exist") {
				t.Fatalf("unknown diagnostic=%q", stderr)
			}
			if state == "backup" && (!strings.Contains(stderr, "sandboxed-agents-backup.default.demo") || !strings.Contains(stderr, "update demo")) {
				t.Fatalf("backup diagnostic=%q", stderr)
			}
			for _, kind := range []string{"workspace", "home", "ssh"} {
				if state == "all volumes" || state == kind {
					if !strings.Contains(stdout, "sandboxed-agents.default.demo."+kind+"; owner default") {
						t.Fatalf("missing volume owner: %q", stdout)
					}
				}
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}

// TestCheckWorkspaceBindNeedsNoWorkspaceVolume proves binds require only home and SSH volumes and report any unused workspace volume through the built CLI and fake Podman seam.
func TestCheckWorkspaceBindNeedsNoWorkspaceVolume(t *testing.T) {
	for _, kept := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "kept"}[kept], func(t *testing.T) {
			responses := healthyCheckResponses(false)
			record := responses["container inspect sandboxed-agents.default.demo"]
			record.Output = strings.Replace(record.Output, `"State":`, `"Mounts":[{"Type":"bind","Source":"/project","Destination":"/workspace"}],"State":`, 1)
			responses["container inspect sandboxed-agents.default.demo"] = record
			if !kept {
				responses["volume exists sandboxed-agents.default.demo.workspace"] = lifecycleResponse{Exit: 1}
			}
			fakes := sshSetupHarness(t, responses)
			status, stdout, stderr := sshSetupRun(t, "check", "demo")
			if status != 0 || strings.Contains(stdout, "unused workspace volume") != kept {
				t.Fatalf("bind check=%d %q %q", status, stdout, stderr)
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}

// TestCheckReadsCPUQuotaAndNanoCPUFormats proves both Podman CPU representations produce the recorded limit through the built CLI and fake Podman seam.
func TestCheckReadsCPUQuotaAndNanoCPUFormats(t *testing.T) {
	responses := healthyCheckResponses(false)
	record := responses["container inspect sandboxed-agents.default.demo"]
	record.Output = strings.Replace(record.Output, `"CpuQuota":250000,"CpuPeriod":100000`, `"NanoCpus":2500000000`, 1)
	responses["container inspect sandboxed-agents.default.demo"] = record
	fakes := sshSetupHarness(t, responses)
	status, stdout, stderr := sshSetupRun(t, "check", "demo")
	if status != 0 || !strings.Contains(stdout, "cpus 2.5") {
		t.Fatalf("CPU check=%d %q %q", status, stdout, stderr)
	}
	assertCheckQueriesOnly(t, fakes, 0)
}

// TestCheckLargeCPULimitUsesDecimalNotation proves the maximum supported CPU limit is reported in reusable decimal notation through the built CLI and fake Podman seam.
func TestCheckLargeCPULimitUsesDecimalNotation(t *testing.T) {
	responses := healthyCheckResponses(false)
	record := responses["container inspect sandboxed-agents.default.demo"]
	record.Output = strings.Replace(record.Output, `"CpuQuota":250000,"CpuPeriod":100000`, `"NanoCpus":9223372036854000000`, 1)
	responses["container inspect sandboxed-agents.default.demo"] = record
	fakes := sshSetupHarness(t, responses)
	status, stdout, stderr := sshSetupRun(t, "check", "demo")
	if status != 0 || !strings.Contains(stdout, "cpus 9223372036.854\n") {
		t.Fatalf("decimal CPU limit=%d %q %q", status, stdout, stderr)
	}
	assertCheckQueriesOnly(t, fakes, 0)
}

// TestCheckReportsFractionalCPUOverride proves check recovers an override from its truncated quota and preserves ratios that do not match a valid override through the built CLI and fake Podman seam.
func TestCheckReportsFractionalCPUOverride(t *testing.T) {
	for _, test := range []struct{ quota, want string }{{"7099", "0.071"}, {"899", "0.009"}, {"12345", "0.12345"}} {
		t.Run(test.quota, func(t *testing.T) {
			responses := healthyCheckResponses(false)
			record := responses["container inspect sandboxed-agents.default.demo"]
			record.Output = strings.Replace(record.Output, `"CpuQuota":250000`, `"CpuQuota":`+test.quota, 1)
			responses["container inspect sandboxed-agents.default.demo"] = record
			fakes := sshSetupHarness(t, responses)
			status, stdout, stderr := sshSetupRun(t, "check", "demo")
			if status != 0 || !strings.Contains(stdout, "cpus "+test.want+"\n") {
				t.Fatalf("reported CPU override=%d %q %q", status, stdout, stderr)
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}

// TestCheckInspectionFailuresCannotClaimHealth proves missing running state, unreadable inspect data, and absent resource facts cause failure through the built CLI and fake Podman seam.
func TestCheckInspectionFailuresCannotClaimHealth(t *testing.T) {
	for _, field := range []string{"State", "HostConfig", "inspect"} {
		t.Run(field, func(t *testing.T) {
			responses := healthyCheckResponses(false)
			record := responses["container inspect sandboxed-agents.default.demo"]
			want := "invalid inspect answer"
			if field == "inspect" {
				record.Output = "malformed"
			} else {
				var containers []map[string]any
				if err := json.Unmarshal([]byte(record.Output), &containers); err != nil {
					t.Fatal(err)
				}
				delete(containers[0], field)
				data, err := json.Marshal(containers)
				if err != nil {
					t.Fatal(err)
				}
				record.Output = string(data)
				want = "is missing"
			}
			responses["container inspect sandboxed-agents.default.demo"] = record
			fakes := sshSetupHarness(t, responses)
			status, _, stderr := sshSetupRun(t, "check", "demo")
			if status == 0 || !strings.Contains(stderr, want) {
				t.Fatalf("unverified=%d %q", status, stderr)
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}

// TestCheckInstalledSSHSetupReportsAccessProblemsWithoutChanges proves SSH access, stopped state, and stale setup diagnostics through the built CLI, fake SSH, and isolated filesystem seams.
func TestCheckInstalledSSHSetupReportsAccessProblemsWithoutChanges(t *testing.T) {
	for _, state := range []string{"reachable", "SSH failure", "stopped", "volumes only", "missing", "manager unavailable"} {
		t.Run(state, func(t *testing.T) {
			fakes := sshSetupHarness(t, healthyCheckResponses(true))
			if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
				t.Fatalf("install=%d %q %q", status, stdout, stderr)
			}
			responses := healthyCheckResponses(state != "stopped")
			if state == "volumes only" || state == "missing" {
				responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
			}
			if state == "missing" {
				for _, kind := range []string{"workspace", "home", "ssh"} {
					responses["volume exists sandboxed-agents.default.demo."+kind] = lifecycleResponse{Exit: 1}
				}
			}
			if state == "manager unavailable" {
				responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Exit: 42}
			}
			scriptSetupResponses(t, responses, runtime.GOOS)
			t.Setenv("TEST_SSH_EXIT", "0")
			if state == "SSH failure" {
				t.Setenv("TEST_SSH_EXIT", "255")
				t.Setenv("TEST_SSH_OUTPUT", "Permission denied (publickey)")
			}
			before := setupFiles(t)
			podmanBefore := len(testutil.Invocations(t, fakes.PodmanLog))
			sshBefore := len(testutil.Invocations(t, fakes.SSHLog))
			status, stdout, stderr := sshSetupRun(t, "check", "demo")
			problem := state == "SSH failure" || state == "volumes only" || state == "missing" || state == "manager unavailable"
			if (status != 0) != problem {
				t.Fatalf("check=%d %q %q", status, stdout, stderr)
			}
			if state == "SSH failure" && (!strings.Contains(stderr, "SSH connection failed") || !strings.Contains(stderr, "Permission denied")) {
				t.Fatalf("SSH failure diagnostic=%q", stderr)
			}
			if (state == "volumes only" || state == "missing") && !strings.Contains(stderr, "ssh-config demo --remove") {
				t.Fatalf("stale setup diagnostic=%q", stderr)
			}
			calls := testutil.Invocations(t, fakes.SSHLog)[sshBefore:]
			probe := state == "reachable" || state == "SSH failure" || state == "manager unavailable"
			if (len(calls) == 1) != probe || len(calls) > 1 {
				t.Fatalf("SSH probes=%v", calls)
			}
			if probe {
				joined := strings.Join(calls[0], " ")
				for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "UpdateHostKeys=no", "ConnectTimeout=3", "ControlPath=none", "demo", "true"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("missing read-only SSH option %q: %v", want, calls)
					}
				}
			}
			if !reflect.DeepEqual(before, setupFiles(t)) {
				t.Fatal("check changed installed SSH files")
			}
			assertCheckQueriesOnly(t, fakes, podmanBefore)
		})
	}
}

// assertCheckQueriesOnly permits sandbox inspection and the manager's read-only version query.
func assertCheckQueriesOnly(t *testing.T, fakes testutil.Fakes, after int) {
	t.Helper()
	for _, call := range testutil.Invocations(t, fakes.PodmanLog)[after:] {
		if len(call) > 1 && call[0] == "--connection" {
			call = call[2:]
		}
		manager := strings.Join(call, " ") == "exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"
		if !sandboxObjectQuery(call) && !manager {
			t.Fatalf("check issued a non-query Podman call: %v", call)
		}
	}
}

// TestCheckHealthySandboxReportsResourcesWithoutChanges proves running and stopped sandbox diagnostics and an absent SSH setup succeed through the built CLI and fake Podman seam.
func TestCheckHealthySandboxReportsResourcesWithoutChanges(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
			fakes := sshSetupHarness(t, healthyCheckResponses(running))
			before := setupFiles(t)
			status, stdout, stderr := sshSetupRun(t, "check", "demo")
			if status != 0 || stderr != "" {
				t.Fatalf("check=%d %q %q", status, stdout, stderr)
			}
			for _, want := range []string{"container sandboxed-agents.default.demo", "owner default", "memory 9663676416", "cpus 2.5", "pids-limit 3000", "shm-size 2147483648", "SSH setup: not installed"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("missing %q: %s", want, stdout)
				}
			}
			manager := "manager: not checked (sandbox stopped)"
			if running {
				manager = "manager: reachable"
			}
			if !strings.Contains(stdout, manager) {
				t.Fatalf("missing %q: %s", manager, stdout)
			}
			if !reflect.DeepEqual(before, setupFiles(t)) {
				t.Fatal("check changed host files")
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("uninstalled setup queried SSH: %v", calls)
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}

// TestCheckReportsMissingVolumes proves a container's absent volumes are named as problems through the built CLI and fake Podman seam.
func TestCheckReportsMissingVolumes(t *testing.T) {
	for _, kind := range []string{"workspace", "home", "ssh"} {
		t.Run(kind, func(t *testing.T) {
			responses := healthyCheckResponses(false)
			name := "sandboxed-agents.default.demo." + kind
			responses["volume exists "+name] = lifecycleResponse{Exit: 1}
			fakes := sshSetupHarness(t, responses)
			status, _, stderr := sshSetupRun(t, "check", "demo")
			if status == 0 || !strings.Contains(stderr, "missing volume "+name) {
				t.Fatalf("missing volume=%d %q", status, stderr)
			}
			assertCheckQueriesOnly(t, fakes, 0)
		})
	}
}
