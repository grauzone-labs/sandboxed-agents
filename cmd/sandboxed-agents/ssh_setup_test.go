package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func sshSetupRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Env = os.Environ()
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return exitStatus(t, err), stdout.String(), stderr.String()
}

// TestUpSSHOptInRefusesWorkspaceChangeBeforeStarting proves opt-in reuse does not silently substitute an existing workspace for a newly requested bind through RunWithHost and fake Podman inspection.
func TestUpSSHOptInRefusesWorkspaceChangeBeforeStarting(t *testing.T) {
	responses := setupPreflightResponses(false)
	fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
	scriptSetupResponses(t, responses, "linux")
	host := lifecycleLinuxHost(t, fakes)
	var stdout, stderr strings.Builder
	workspace := t.TempDir()
	status := cli.RunWithHost([]string{"up", "demo", workspace, "--ssh-config"}, &stdout, &stderr, host)
	if status == 0 || !strings.Contains(stderr.String(), "workspace") || !strings.Contains(stderr.String(), "remove demo") || !strings.Contains(stderr.String(), "up demo") {
		t.Fatalf("changed workspace=%d %q %q", status, stdout.String(), stderr.String())
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && (call[0] == "start" || call[0] == "exec") {
			t.Fatalf("workspace conflict started sandbox=%v", call)
		}
	}
}

// TestRunningUpSSHOptInReportsUnavailableManagerBeforeOptionConflict proves the running sandbox's manager precondition wins over changed configuration through RunWithHost and fake Podman.
func TestRunningUpSSHOptInReportsUnavailableManagerBeforeOptionConflict(t *testing.T) {
	responses := setupPreflightResponses(true)
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Exit: 42, Output: "unavailable manager"}
	fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
	scriptSetupResponses(t, responses, "linux")
	host := lifecycleLinuxHost(t, fakes)
	var stdout, stderr strings.Builder
	status := cli.RunWithHost([]string{"up", "demo", "--ssh-config", "--port", "2300", "--memory", "9g"}, &stdout, &stderr, host)
	if status == 0 || !strings.Contains(stderr.String(), "unavailable manager") || !strings.Contains(stderr.String(), "check demo") || !strings.Contains(stderr.String(), "restart demo") || strings.Contains(stderr.String(), "differs from") {
		t.Fatalf("precedence=%d %q %q", status, stdout.String(), stderr.String())
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && (call[0] == "start" || call[0] == "stop" || call[0] == "rm") {
			t.Fatalf("precondition changed state=%v", call)
		}
	}
}

// TestUpSSHOptInChecksExplicitResourceLimitsBeforeStarting proves opt-in reuse refuses changed resource limits and accepts matching limits through RunWithHost and fake Podman inspection.
func TestUpSSHOptInChecksExplicitResourceLimitsBeforeStarting(t *testing.T) {
	for _, options := range [][]string{{"--memory", "9g"}, {"--cpus", "5"}, {"--pids-limit", "2000"}, {"--shm-size", "2g"}, {"--memory", "8g", "--cpus", "4", "--pids-limit", "2048", "--shm-size", "1g"}} {
		t.Run(strings.Join(options, " "), func(t *testing.T) {
			responses := setupPreflightResponses(true)
			responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}},"HostConfig":{"Memory":8589934592,"MemorySwap":8589934592,"NanoCpus":4000000000,"CpuQuota":400000,"CpuPeriod":100000,"PidsLimit":2048,"ShmSize":1073741824},"State":{"Running":false}}]`}
			fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
			scriptSetupResponses(t, responses, "linux")
			host := lifecycleLinuxHost(t, fakes)
			host.PortAvailable = func(uint16) error { return nil }
			var stdout, stderr strings.Builder
			status := cli.RunWithHost(append([]string{"up", "demo", "--ssh-config"}, options...), &stdout, &stderr, host)
			matching := len(options) > 2
			if matching {
				if status != 0 {
					t.Fatalf("matching=%d %q %q", status, stdout.String(), stderr.String())
				}
			} else {
				if status == 0 || !strings.Contains(stderr.String(), options[0]) {
					t.Fatalf("changed=%d %q %q", status, stdout.String(), stderr.String())
				}
				for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
					if len(call) > 0 && (call[0] == "start" || call[0] == "exec") {
						t.Fatalf("changed limits started sandbox=%v", call)
					}
				}
			}
		})
	}
}

// TestSSHSetupStoppedRemovalThenReinstallReplacesAuthorization proves stopped cleanup skips manager calls and the next installation authorizes the newly generated dedicated key through the built CLI and fake process seams.
func TestSSHSetupStoppedRemovalThenReinstallReplacesAuthorization(t *testing.T) {
	fakes := sshSetupHarness(t, installResponses())
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("install=%d %q %q", status, stdout, stderr)
	}
	selectSetupSandbox(t, "default", "demo", false, false)
	before := len(testutil.Invocations(t, fakes.PodmanLog))
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove"); status != 0 || !strings.Contains(stdout, "authorization remains") {
		t.Fatalf("stopped removal=%d %q %q", status, stdout, stderr)
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog)[before:] {
		if strings.Contains(strings.Join(call, " "), " exec ") || len(call) > 0 && call[0] == "exec" {
			t.Fatalf("stopped manager call=%v", call)
		}
	}
	newKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB"
	t.Setenv("TEST_SSH_KEYGEN_PUBLIC_KEY", newKey)
	responses := installResponses()
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-authorize "+newKey] = lifecycleResponse{}
	scriptSetupResponses(t, responses, runtime.GOOS)
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("reinstall=%d %q %q", status, stdout, stderr)
	}
	var authorized []string
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		for index, arg := range call {
			if arg == "ssh-authorize" {
				authorized = append(authorized, call[index+1])
			}
		}
	}
	if !reflect.DeepEqual(authorized, []string{setupPublicKey, newKey}) {
		t.Fatalf("authorizations=%v", authorized)
	}
}

// TestSSHSetupIncludeEditsOnlyCompleteManagedLines proves cleanup preserves a user comment containing the managed Include text through the built CLI and isolated configuration filesystem seam.
func TestSSHSetupIncludeEditsOnlyCompleteManagedLines(t *testing.T) {
	sshSetupHarness(t, installResponses())
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("install=%d %q %q", status, stdout, stderr)
	}
	path := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	line, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	comment := append([]byte("# user comment: "), line...)
	if err := os.WriteFile(path, append(append([]byte(nil), line...), comment...), 0600); err != nil {
		t.Fatal(err)
	}
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove"); status != 0 {
		t.Fatalf("remove=%d %q %q", status, stdout, stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(data, comment) {
		t.Fatalf("comment changed=%q want=%q error=%v", data, comment, err)
	}
}

// TestUpWithoutSSHOptInLeavesUserSSHFilesUnchanged proves successful sandbox creation leaves every preexisting user SSH byte untouched and runs no SSH command through RunWithHost and fake process seams.
func TestUpWithoutSSHOptInLeavesUserSSHFilesUnchanged(t *testing.T) {
	responses := setupNewSandboxResponses()
	fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
	scriptSetupResponses(t, responses, "linux")
	dir := filepath.Join(os.Getenv("HOME"), ".ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"config": "Host personal\n HostName example.com\n", "id_secret": "personal secret bytes", "known_hosts": "personal pin bytes"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := setupFiles(t)
	host := lifecycleLinuxHost(t, fakes)
	host.PortAvailable = func(uint16) error { return nil }
	var stdout, stderr strings.Builder
	if status := cli.RunWithHost([]string{"up", "demo"}, &stdout, &stderr, host); status != 0 {
		t.Fatalf("up=%d %q %q", status, stdout.String(), stderr.String())
	}
	if after := setupFiles(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("SSH files changed before=%v after=%v", before, after)
	}
	if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("SSH process=%v", calls)
	}
}

// TestUpSSHOptInRefusesChangedRecordedPortBeforeStarting proves opt-in reuse refuses a conflicting recorded port before starting through RunWithHost and fake Podman.
func TestUpSSHOptInRefusesChangedRecordedPortBeforeStarting(t *testing.T) {
	responses := setupPreflightResponses(false)
	fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
	scriptSetupResponses(t, responses, "linux")
	host := lifecycleLinuxHost(t, fakes)
	var stdout, stderr strings.Builder
	status := cli.RunWithHost([]string{"up", "demo", "--ssh-config", "--port", "2300"}, &stdout, &stderr, host)
	if status == 0 || !strings.Contains(stderr.String(), "port") || !strings.Contains(stderr.String(), "remove demo") || !strings.Contains(stderr.String(), "up demo") {
		t.Fatalf("conflict=%d %q %q", status, stdout.String(), stderr.String())
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && (call[0] == "start" || call[0] == "exec") {
			t.Fatalf("effect before option refusal=%v", call)
		}
	}
}

// TestSSHSetupRealOpenSSHResolvesIncludesAndPreservesConfigAlias proves quoted paths, configuration aliases, wildcard defaults, existing modes, and actual configured-host conflicts through the built CLI with real Linux ssh -G and fake Podman.
func TestSSHSetupRealOpenSSHResolvesIncludesAndPreservesConfigAlias(t *testing.T) {
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("nested offline guard isolates PATH to fake programs; the outer suite runs this real OpenSSH configuration check")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux real-OpenSSH query check; Windows ACL/OpenSSH checks run in the Windows adapter suite")
	}
	realSSH, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is unavailable")
	}
	binary, err := os.ReadFile(realSSH)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"alias-path-spaces", "manual", "wildcard-keepalive", "wildcard-identity-agent", "wildcard-hostname", "other-group"} {
		t.Run(kind, func(t *testing.T) {
			fakes := sshSetupHarness(t, installResponses())
			if err := os.WriteFile(filepath.Join(fakes.Dir, "ssh"), binary, 0700); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "state with spaces %literal")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", state)
			config := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
			if err := os.MkdirAll(filepath.Dir(config), 0750); err != nil {
				t.Fatal(err)
			}
			original := []byte("# personal\nHost personal\n HostName example.com\n")
			if kind == "manual" {
				original = []byte("Host demo\n HostName example.com\n")
			}
			if kind == "wildcard-keepalive" {
				original = []byte("Host *\n ServerAliveInterval 60\n")
			}
			if kind == "wildcard-identity-agent" {
				original = []byte("Host *\n IdentityAgent none\n")
			}
			if kind == "wildcard-hostname" {
				original = []byte("Host *\n HostName example.com\n")
			}
			target := filepath.Join(t.TempDir(), "config alias target")
			if err := os.WriteFile(target, original, 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, config); err != nil {
				t.Fatal(err)
			}
			if kind == "other-group" {
				selectSetupSandbox(t, "live", "demo", true, false)
				if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
					t.Fatalf("other install=%d %q %q", status, stdout, stderr)
				}
				selectSetupSandbox(t, "default", "demo.live", true, false)
			}
			before := setupFiles(t)
			name := "demo"
			if kind == "other-group" {
				name = "demo.live"
			}
			status, stdout, stderr := sshSetupRun(t, "ssh-config", name, "--install")
			if kind == "manual" || kind == "wildcard-hostname" || kind == "other-group" {
				if status == 0 || !strings.Contains(stderr, "conflict") || !strings.Contains(stderr, name) {
					t.Fatalf("conflict=%d %q %q", status, stdout, stderr)
				}
				if after := setupFiles(t); !reflect.DeepEqual(before, after) {
					t.Fatal("conflict changed files")
				}
				return
			}
			if status != 0 {
				t.Fatalf("quoted install=%d %q %q", status, stdout, stderr)
			}
			query := exec.Command(realSSH, "-G", "-F", config, "demo")
			output, err := query.Output()
			if err != nil || !strings.Contains(string(output), "user agent\n") || !strings.Contains(string(output), "hostname 127.0.0.1\n") || !strings.Contains(string(output), "port 2222\n") {
				t.Fatalf("resolved=%q error=%v", output, err)
			}
			info, err := os.Lstat(config)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("config alias replaced=%v %v", info, err)
			}
			info, err = os.Stat(target)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("target mode=%v %v", info, err)
			}
			data, err := os.ReadFile(target)
			if err != nil || !strings.HasSuffix(string(data), string(original)) || strings.Count(string(data), "Include ") != 1 {
				t.Fatalf("target bytes=%q %v", data, err)
			}
		})
	}
}

// TestRemoveCleansSSHSetupForContainerAndVolumesOnly proves lifecycle and access removal clean all host files after permitted deletion while kept volumes retain authorization through the built CLI and fake Podman seam.
func TestRemoveCleansSSHSetupForContainerAndVolumesOnly(t *testing.T) {
	for _, kind := range []string{"container-keeps-volumes", "container-deletes-owned", "container-keeps-foreign", "only-volumes-keep", "only-volumes-delete", "only-volumes-ssh-remove"} {
		t.Run(kind, func(t *testing.T) {
			sshSetupHarness(t, installResponses())
			if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
				t.Fatalf("install=%d %q %q", status, stdout, stderr)
			}
			responses := installResponses()
			for _, suffix := range []string{"home", "ssh"} {
				name := "sandboxed-agents.default.demo." + suffix
				owner := "default"
				if kind == "container-keeps-foreign" && suffix == "home" {
					owner = "foreign"
				}
				responses["volume exists "+name] = lifecycleResponse{}
				responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				responses["volume rm "+name] = lifecycleResponse{}
			}
			if strings.HasPrefix(kind, "only-volumes") {
				responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
			}
			scriptSetupResponses(t, responses, runtime.GOOS)
			args := []string{"remove", "demo"}
			if strings.Contains(kind, "delete") || kind == "container-keeps-foreign" {
				args = append(args, "--volumes")
			}
			if kind == "only-volumes-ssh-remove" {
				args = []string{"ssh-config", "demo", "--remove"}
			}
			status, stdout, stderr := sshSetupRun(t, args...)
			if (status != 0) != (kind == "container-keeps-foreign") {
				t.Fatalf("remove=%d %q %q", status, stdout, stderr)
			}
			if kind == "only-volumes-keep" && (!strings.Contains(stdout, "has no container") || !strings.Contains(stdout, "use remove demo --volumes")) {
				t.Fatalf("missing volume cleanup guidance=%q", stdout)
			}
			if kind == "only-volumes-delete" {
				if strings.Contains(stdout, "use remove demo --volumes") {
					t.Fatalf("deleted volumes still request deletion=%q", stdout)
				}
				for _, suffix := range []string{"home", "ssh"} {
					if !strings.Contains(stdout, "removed volume sandboxed-agents.default.demo."+suffix) {
						t.Fatalf("removed volume not named=%q", stdout)
					}
				}
			}
			if strings.Contains(kind, "keep") && kind != "container-keeps-foreign" && !strings.Contains(stdout, "authorization remains") {
				t.Fatalf("retained authorization not named=%q", stdout)
			}
			for path := range setupFiles(t) {
				if filepath.Base(path) != "config" {
					t.Fatalf("key or entry retained=%s", path)
				}
			}
			config, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ssh", "config"))
			if err != nil || len(config) != 0 {
				t.Fatalf("group Include retained=%q %v", config, err)
			}
		})
	}
}

// TestLifecycleSSHOptInInstallsAfterStartAndKeepsSandboxOnFailure proves up/start opt-in setup and explicit retry without stopping or deleting a running sandbox through RunWithHost and fake process seams.
func TestLifecycleSSHOptInInstallsAfterStartAndKeepsSandboxOnFailure(t *testing.T) {
	for _, kind := range []string{"new-up", "existing-up", "start"} {
		for _, fail := range []bool{false, true} {
			t.Run(kind+strconv.FormatBool(fail), func(t *testing.T) {
				responses := setupPreflightResponses(false)
				if kind == "new-up" {
					responses = setupNewSandboxResponses()
				}
				if fail {
					responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Exit: 42, Output: "unavailable manager"}
				}
				// This seam deliberately exercises the Linux host adapter even on a Windows runner.
				fakes := sshSetupHarness(t, map[string]lifecycleResponse{})
				scriptSetupResponses(t, responses, "linux")
				host := lifecycleLinuxHost(t, fakes)
				host.PortAvailable = func(uint16) error { return nil }
				verb := "up"
				if kind == "start" {
					verb = "start"
				}
				var stdout, stderr strings.Builder
				status := cli.RunWithHost([]string{verb, "demo", "--ssh-config"}, &stdout, &stderr, host)
				if fail {
					if status == 0 || !strings.Contains(stderr.String(), "ssh-config demo --install") || !strings.Contains(stderr.String(), "unavailable manager") {
						t.Fatalf("failed=%d %q %q", status, stdout.String(), stderr.String())
					}
				} else if status != 0 || !strings.Contains(stdout.String(), "installed SSH setup") {
					t.Fatalf("success=%d %q %q", status, stdout.String(), stderr.String())
				}
				calls := testutil.Invocations(t, fakes.PodmanLog)
				started := false
				for _, call := range calls {
					if len(call) > 0 && call[0] == "start" {
						started = true
					}
					if len(call) > 0 && (call[0] == "stop" || call[0] == "rm" || (call[0] == "container" && len(call) > 1 && call[1] == "rm")) {
						t.Fatalf("setup failure rolled back running sandbox=%v", calls)
					}
				}
				if !started {
					t.Fatalf("sandbox not started=%v", calls)
				}
			})
		}
	}
}

func setupFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, root := range []string{filepath.Join(os.Getenv("HOME"), ".ssh"), filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents")} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				if strings.Contains(path, string(filepath.Separator)+"locks"+string(filepath.Separator)) {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// TestSSHSetupUnavailableManagerRemovalCleansHost proves a running unavailable manager causes nonzero authorization diagnostics after complete host cleanup through the built CLI and fake Podman seam.
func TestSSHSetupUnavailableManagerRemovalCleansHost(t *testing.T) {
	fakes := sshSetupHarness(t, installResponses())
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("install=%d %q %q", status, stdout, stderr)
	}
	selectSetupSandbox(t, "default", "demo", true, true)
	status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove")
	if status == 0 || !strings.Contains(stderr, "authorization in sandbox demo could not be removed") || !strings.Contains(stderr, "check demo") || !strings.Contains(stderr, "restart demo") {
		t.Fatalf("remove=%d %q %q", status, stdout, stderr)
	}
	for path := range setupFiles(t) {
		if filepath.Base(path) != "config" {
			t.Fatalf("setup file remains=%s", path)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ssh", "config"))
	if err != nil || len(data) != 0 {
		t.Fatalf("Include remains=%q %v", data, err)
	}
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && (call[0] == "stop" || call[0] == "start") {
			t.Fatalf("sandbox state changed=%v", call)
		}
	}
}

// TestSSHSetupStateRefusalsPreserveInstalledFiles proves owner and interrupted-update checks win over stopped state and refused removals leave setup files intact through the built CLI and fake Podman seam.
func TestSSHSetupStateRefusalsPreserveInstalledFiles(t *testing.T) {
	for _, kind := range []string{"container-owner", "volume-owner", "backup", "stopped", "only-volumes", "foreign-only-volumes", "sessions"} {
		argsList := [][]string{{"ssh-config", "demo"}, {"ssh-config", "demo", "--install"}, {"ssh-config", "demo", "--remove"}, {"remove", "demo"}, {"remove", "demo", "--volumes"}, {"start", "demo", "--ssh-config"}}
		if kind == "stopped" {
			argsList = [][]string{{"ssh-config", "demo", "--install"}}
		}
		if kind == "only-volumes" {
			argsList = [][]string{{"ssh-config", "demo"}, {"ssh-config", "demo", "--install"}, {"start", "demo", "--ssh-config"}}
		}
		if kind == "volume-owner" {
			argsList = argsList[:4]
		}
		if kind == "sessions" {
			argsList = [][]string{{"remove", "demo"}, {"remove", "demo", "--volumes"}}
		}
		for _, args := range argsList {
			t.Run(kind+strings.Join(args, " "), func(t *testing.T) {
				fakes := sshSetupHarness(t, installResponses())
				if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
					t.Fatalf("install=%d %q %q", status, stdout, stderr)
				}
				before := setupFiles(t)
				responses := installResponses()
				want := "owner conflict"
				response := responses["container inspect sandboxed-agents.default.demo"]
				if kind != "sessions" {
					response.Output = strings.ReplaceAll(response.Output, `"Running":true`, `"Running":false`)
				}
				if kind == "container-owner" {
					response.Output = strings.ReplaceAll(response.Output, `"default"`, `"foreign"`)
				}
				responses["container inspect sandboxed-agents.default.demo"] = response
				if kind == "volume-owner" || strings.Contains(kind, "volumes") {
					responses["volume exists sandboxed-agents.default.demo.home"] = lifecycleResponse{}
					owner := "default"
					if kind == "volume-owner" || kind == "foreign-only-volumes" {
						owner = "foreign"
					}
					responses["volume inspect sandboxed-agents.default.demo.home"] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				}
				if strings.Contains(kind, "only-volumes") {
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
				}
				if kind == "only-volumes" {
					want = "up demo"
				}
				if kind == "stopped" {
					want = "start demo"
				}
				if kind == "backup" {
					want = "update demo"
					responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
					responses["container inspect sandboxed-agents-backup.default.demo"] = response
				}
				if kind == "sessions" {
					want = "running agent sessions"
					responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions"] = lifecycleResponse{Output: "codex\n"}
				}
				scriptSetupResponses(t, responses, runtime.GOOS)
				beforeCalls := len(testutil.Invocations(t, fakes.PodmanLog))
				status, stdout, stderr := sshSetupRun(t, args...)
				if status == 0 || stdout != "" || !strings.Contains(stderr, want) {
					t.Fatalf("refusal=%d %q %q want=%s", status, stdout, stderr, want)
				}
				if after := setupFiles(t); !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed files")
				}
				for _, call := range testutil.Invocations(t, fakes.PodmanLog)[beforeCalls:] {
					joined := strings.Join(call, " ")
					if strings.Contains(joined, "ssh-authorize") || strings.Contains(joined, "ssh-deauthorize") || strings.Contains(joined, "manager version") {
						t.Fatalf("manager side effect before refusal=%v", call)
					}
				}
			})
		}
	}
}

// TestSSHSetupRepeatIsUnchangedAndHostKeyChangeRefusesRepin proves repeat installation preserves every host file and a changed trusted key requires explicit remove/install through the built CLI and fake process seams.
func TestSSHSetupRepeatIsUnchangedAndHostKeyChangeRefusesRepin(t *testing.T) {
	fakes := sshSetupHarness(t, installResponses())
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install"); status != 0 {
		t.Fatalf("install=%d %q %q", status, stdout, stderr)
	}
	before := setupFiles(t)
	sshCalls := testutil.Invocations(t, fakes.SSHLog)
	keyCalls := testutil.Invocations(t, filepath.Join(fakes.Dir, "keygen.jsonl"))
	status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install")
	if status != 0 || !strings.Contains(stdout, "unchanged") {
		t.Fatalf("repeat=%d %q %q", status, stdout, stderr)
	}
	if after := setupFiles(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("files changed before=%v after=%v", before, after)
	}
	if calls := testutil.Invocations(t, fakes.SSHLog); !reflect.DeepEqual(calls, sshCalls) {
		t.Fatalf("repeat queried config=%v", calls)
	}
	if calls := testutil.Invocations(t, filepath.Join(fakes.Dir, "keygen.jsonl")); !reflect.DeepEqual(calls, keyCalls) {
		t.Fatalf("repeat generated key=%v", calls)
	}
	responses := installResponses()
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-host-key"] = lifecycleResponse{Output: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB\n"}
	scriptSetupResponses(t, responses, runtime.GOOS)
	status, stdout, stderr = sshSetupRun(t, "ssh-config", "demo", "--install")
	if status == 0 || !strings.Contains(stderr, "ssh-config demo --remove") || !strings.Contains(stderr, "ssh-config demo --install") {
		t.Fatalf("changed=%d %q %q", status, stdout, stderr)
	}
	if after := setupFiles(t); !reflect.DeepEqual(before, after) {
		t.Fatal("changed host key modified host files")
	}
}

// TestSSHSetupConflictsAndQueryFailuresWriteNothing proves resolved-host conflicts, query errors, and manager failures refuse before SSH file writes through the built CLI and fake SSH seam.
func TestSSHSetupConflictsAndQueryFailuresWriteNothing(t *testing.T) {
	for _, kind := range []string{"configured-host", "query-failure", "manager-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			responses := installResponses()
			if kind == "manager-unavailable" {
				responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Exit: 42}
			}
			fakes := sshSetupHarness(t, responses)
			config := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
			if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("# user bytes\nHost manual\n    HostName example.com\n")
			if err := os.WriteFile(config, original, 0640); err != nil {
				t.Fatal(err)
			}
			response := lifecycleResponse{Output: "hostname configured\n"}
			if kind == "query-failure" {
				response = lifecycleResponse{Exit: 23, Output: "configuration syntax error"}
			}
			queries := map[string]lifecycleResponse{"-G -F " + config + " demo": response, "-G -F none demo": {Output: "hostname demo\n"}}
			script, err := json.Marshal(queries)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_SSH_RESPONSES", string(script))
			status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install")
			if status == 0 || stdout != "" {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			want := "conflict"
			if kind == "query-failure" {
				want = "configuration syntax error"
			}
			if kind == "manager-unavailable" {
				want = "check demo"
				if !strings.Contains(stderr, "restart demo") {
					t.Fatalf("manager diagnostic=%q", stderr)
				}
			}
			if !strings.Contains(stderr, want) || !strings.Contains(stderr, "demo") {
				t.Fatalf("stderr=%q", stderr)
			}
			if data, err := os.ReadFile(config); err != nil || string(data) != string(original) {
				t.Fatalf("config=%q error=%v", data, err)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "group-default", "ssh")); !os.IsNotExist(err) {
				t.Fatalf("SSH setup state changed: %v", err)
			}
			if calls := testutil.Invocations(t, filepath.Join(fakes.Dir, "keygen.jsonl")); len(calls) != 0 {
				t.Fatalf("key generated=%v", calls)
			}
		})
	}
}

// TestSSHConfigPreviewPrintsHardenedEntryWithoutWriting proves prospective host entries on running and stopped sandboxes through the built CLI and isolated host filesystem seam.
func TestSSHConfigPreviewPrintsHardenedEntryWithoutWriting(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "stopped"}[running], func(t *testing.T) {
			responses := setupResponses(running)
			fakes := sshSetupHarness(t, responses)
			status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo")
			if status != 0 || !strings.Contains(stdout, "Host demo\n") || !strings.Contains(stderr, "not installed") || !strings.Contains(stderr, "ssh-config demo --install") {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			for _, line := range []string{"HostName 127.0.0.1", "Port 2222", "User agent", "IdentitiesOnly yes", "IdentityAgent none", "ForwardAgent no", "StrictHostKeyChecking yes", "UserKnownHostsFile", "IdentityFile"} {
				if !strings.Contains(stdout, line) {
					t.Fatalf("missing %s: %q", line, stdout)
				}
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".ssh")); !os.IsNotExist(err) {
				t.Fatalf("SSH directory created: %v", err)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents")); !os.IsNotExist(err) {
				t.Fatalf("host state created: %v", err)
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("SSH calls=%v", calls)
			}
		})
	}
}

func sshSetupHarness(t *testing.T, responses map[string]lifecycleResponse) testutil.Fakes {
	t.Helper()
	responses = setupResponsesForPlatform(responses, runtime.GOOS)

	fakes := lifecycleHarness(t, responses)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	data, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakes.Dir, "ssh-keygen"+suffix), data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SSH_KEYGEN_LOG", filepath.Join(fakes.Dir, "keygen.jsonl"))
	t.Setenv("TEST_SSH_KEYGEN_PUBLIC_KEY", setupPublicKey)
	return fakes
}

func setupResponsesForPlatform(responses map[string]lifecycleResponse, platform string) map[string]lifecycleResponse {
	if platform != "windows" {
		return responses
	}
	result := map[string]lifecycleResponse{"machine list --format json": {Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`}, "machine inspect chosen": {Output: `[{"Name":"chosen","State":"running","Rootful":false}]`}}
	for key, value := range responses {
		result["--connection chosen "+key] = value
	}
	return result
}

const setupPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func selectSetupSandbox(t *testing.T, group, name string, running bool, managerDown bool) {
	t.Helper()
	responses := map[string]lifecycleResponse{}
	for key, response := range setupResponses(running) {
		key = strings.ReplaceAll(key, ".default.demo", "."+group+"."+name)
		response.Output = strings.ReplaceAll(response.Output, `"default"`, `"`+group+`"`)
		if managerDown && strings.HasSuffix(key, "manager version") {
			response.Exit = 42
		}
		responses[key] = response
	}
	scriptSetupResponses(t, responses, runtime.GOOS)
	t.Setenv("SANDBOXED_AGENTS_GROUP", group)
}

// TestSSHSetupRemovalMaintainsGroupIncludesAndOtherEntries proves one Include per group, preserved user bytes/mode, and targeted host cleanup through the built CLI and isolated filesystem seam.
func TestSSHSetupRemovalMaintainsGroupIncludesAndOtherEntries(t *testing.T) {
	fakes := sshSetupHarness(t, installResponses())
	config := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(config), 0750); err != nil {
		t.Fatal(err)
	}
	original := []byte("# user bytes stay\nHost personal\n  HostName example.com\n")
	if err := os.WriteFile(config, original, 0640); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"default", "demo"}, {"default", "second"}, {"live", "demo"}} {
		selectSetupSandbox(t, pair[0], pair[1], true, false)
		if status, stdout, stderr := sshSetupRun(t, "ssh-config", pair[1], "--install"); status != 0 {
			t.Fatalf("install %v=%d %q %q", pair, status, stdout, stderr)
		}
	}
	data, err := os.ReadFile(config)
	if err != nil || strings.Count(string(data), "Include ") != 2 || !strings.HasSuffix(string(data), string(original)) {
		t.Fatalf("config=%q error=%v", data, err)
	}
	selectSetupSandbox(t, "default", "demo", false, false)
	status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove")
	if status != 0 || !strings.Contains(stdout, "authorization remains") {
		t.Fatalf("remove=%d %q %q", status, stdout, stderr)
	}
	files := setupFiles(t)
	for path, content := range files {
		if strings.HasSuffix(path, "entry") && strings.Contains(content, "Host demo\n") {
			t.Fatalf("requested entry retained=%s", path)
		}
	}
	data, err = os.ReadFile(config)
	if err != nil || strings.Count(string(data), "Include ") != 2 {
		t.Fatalf("group removed early: %q %v", data, err)
	}
	selectSetupSandbox(t, "default", "second", true, false)
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "second", "--remove"); status != 0 {
		t.Fatalf("remove second=%d %q %q", status, stdout, stderr)
	}
	data, err = os.ReadFile(config)
	if err != nil || strings.Count(string(data), "Include ") != 1 || !strings.Contains(string(data), "group-live") {
		t.Fatalf("wrong group include=%q %v", data, err)
	}
	selectSetupSandbox(t, "live", "demo", true, false)
	if status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--remove"); status != 0 {
		t.Fatalf("remove live=%d %q %q", status, stdout, stderr)
	}
	data, err = os.ReadFile(config)
	if err != nil || string(data) != string(original) {
		t.Fatalf("user changed=%q %v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(config)
		if err != nil || info.Mode().Perm() != 0640 {
			t.Fatalf("config mode=%v %v", info, err)
		}
		info, err = os.Stat(filepath.Dir(config))
		if err != nil || info.Mode().Perm() != 0750 {
			t.Fatalf("directory mode=%v %v", info, err)
		}
	}
	var removals [][]string
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if len(call) > 0 && call[len(call)-1] == "ssh-deauthorize" {
			removals = append(removals, call)
		}
	}
	want := [][]string{{"exec", "--user=0:0", "sandboxed-agents.default.second", "/usr/local/bin/sandboxed-agents-manager", "ssh-deauthorize"}, {"exec", "--user=0:0", "sandboxed-agents.live.demo", "/usr/local/bin/sandboxed-agents-manager", "ssh-deauthorize"}}
	if runtime.GOOS == "windows" {
		for i := range want {
			want[i] = append([]string{"--connection", "chosen"}, want[i]...)
		}
	}
	if !reflect.DeepEqual(removals, want) {
		t.Fatalf("root authorization removals=%v want=%v", removals, want)
	}
}

func scriptSetupResponses(t *testing.T, responses map[string]lifecycleResponse, platform string) {
	t.Helper()
	data, err := json.Marshal(setupResponsesForPlatform(responses, platform))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
}

func setupPreflightResponses(running bool) map[string]lifecycleResponse {
	responses := setupResponses(running)
	responses["--version"] = lifecycleResponse{Output: "podman version 4.4.0"}
	responses["info --format json"] = lifecycleResponse{Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`}
	return responses
}

func setupNewSandboxResponses() map[string]lifecycleResponse {
	responses := setupPreflightResponses(true)
	responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
	responses["ps --all --format json"] = lifecycleResponse{Output: "[]"}
	responses["image exists *"] = lifecycleResponse{}
	responses["volume create *"] = lifecycleResponse{}
	responses["create *"] = lifecycleResponse{}
	return responses
}

func installResponses() map[string]lifecycleResponse { return setupResponses(true) }

func setupResponses(running bool) map[string]lifecycleResponse {
	responses := ownedLifecycleResponses(running)
	responses["container inspect sandboxed-agents.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}},"State":{"Running":` + strconv.FormatBool(running) + `}}]`}
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager version"] = lifecycleResponse{Output: "sandboxed-agents-manager development\n"}
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-host-key"] = lifecycleResponse{Output: setupPublicKey + " host\n"}
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-authorize "+setupPublicKey] = lifecycleResponse{}
	responses["exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager ssh-deauthorize"] = lifecycleResponse{}
	return responses
}

// TestSSHSetupInstallCreatesDedicatedKeyPinAndSingleInclude proves opt-in key generation, root authorization, pinned host identity, and minimal user configuration changes through the built CLI and fake process seams.
func TestSSHSetupInstallCreatesDedicatedKeyPinAndSingleInclude(t *testing.T) {
	fakes := sshSetupHarness(t, installResponses())
	userConfig := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	status, stdout, stderr := sshSetupRun(t, "ssh-config", "demo", "--install")
	if status != 0 || !strings.Contains(stdout, "installed SSH setup") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	config, err := os.ReadFile(userConfig)
	if err != nil || strings.Count(string(config), "Include ") != 1 || !strings.HasPrefix(string(config), "Include ") {
		t.Fatalf("config=%q error=%v", config, err)
	}
	status, entry, stderr := sshSetupRun(t, "ssh-config", "demo")
	if status != 0 || stderr != "" || !strings.Contains(entry, "Host demo\n") {
		t.Fatalf("preview=%d %q %q", status, entry, stderr)
	}
	keys := testutil.Invocations(t, filepath.Join(fakes.Dir, "keygen.jsonl"))
	if len(keys) != 1 || len(keys[0]) != 6 || strings.Join(keys[0][:4], " ") != "-t ed25519 -N " || keys[0][4] != "-f" {
		t.Fatalf("keygen=%v", keys)
	}
	keyPath, err := strconv.Unquote(strings.Split(strings.Split(entry, "IdentityFile ")[1], "\n")[0])
	if err != nil {
		t.Fatal(err)
	}
	pin, err := os.ReadFile(filepath.Join(filepath.Dir(keyPath), "known_hosts"))
	if err != nil || string(pin) != "[127.0.0.1]:2222 "+setupPublicKey+"\n" {
		t.Fatalf("pin=%q error=%v", pin, err)
	}
	found := false
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		found = found || strings.HasSuffix(strings.Join(call, " "), "ssh-authorize "+setupPublicKey)
	}
	if !found {
		t.Fatal("manager did not authorize dedicated key")
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{keyPath: 0600, keyPath + ".pub": 0644, filepath.Join(filepath.Dir(keyPath), "known_hosts"): 0600, filepath.Join(filepath.Dir(filepath.Dir(keyPath)), "config"): 0600, userConfig: 0600, filepath.Dir(userConfig): 0700, filepath.Dir(keyPath): 0700} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("path=%s info=%v error=%v want=%o", path, info, err, mode)
			}
		}
	}
}

// TestRemoveVolumesDeletesOwnedVolumesAndKeepsForeignVolumes proves owned-volume deletion and the container-present foreign-volume exception through the built CLI and fake Podman seam.
func TestRemoveVolumesDeletesOwnedVolumesAndKeepsForeignVolumes(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "foreign"}[foreign], func(t *testing.T) {
			responses := ownedLifecycleResponses(true)
			for _, suffix := range []string{"workspace", "home", "ssh"} {
				name := "sandboxed-agents.default.demo." + suffix
				owner := "default"
				if foreign && suffix == "home" {
					owner = "other"
				}
				responses["volume exists "+name] = lifecycleResponse{}
				responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
				responses["volume rm "+name] = lifecycleResponse{}
			}
			fakes := sshSetupHarness(t, responses)
			status, stdout, stderr := sshSetupRun(t, "remove", "demo", "--volumes")
			if (status != 0) != foreign || !strings.Contains(stdout, "removed sandbox demo") {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
			}
			if foreign && (!strings.Contains(stderr, "sandboxed-agents.default.demo.home") || !strings.Contains(stderr, "Podman")) {
				t.Fatalf("conflict=%q", stderr)
			}
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if foreign && strings.Join(call, " ") == "volume rm sandboxed-agents.default.demo.home" {
					t.Fatalf("foreign volume deleted: %v", call)
				}
			}
			for _, suffix := range []string{"workspace", "ssh"} {
				if !strings.Contains(stdout, "removed volume sandboxed-agents.default.demo."+suffix) {
					t.Fatalf("owned volume not removed: %q", stdout)
				}
			}
		})
	}
}
