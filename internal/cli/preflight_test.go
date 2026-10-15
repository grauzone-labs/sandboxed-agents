package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

var fakeBinary string

// TestMain builds the external-process fake used by the exported host CLI seam.
func TestMain(m *testing.M) {
	if os.Getenv("TEST_UP_EXECUTABLE") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "sandboxed-agents-host-test-")
	if err != nil {
		panic(err)
	}
	fakeBinary = filepath.Join(dir, "fake")
	command := exec.Command("go", "build", "-o", fakeBinary, "../testutil/testdata/fake.go")
	if output, err := command.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type response struct {
	FailOnce      bool
	Output        string
	Stderr        string
	Exit          int
	ExpectedStdin string
	EchoStdin     bool
	ReadyFile     string
	WaitFor       string
}
type fixture struct {
	host      cli.Host
	fakes     testutil.Fakes
	files     map[string]string
	responses map[string]response
}

const linuxSubtreeControl = "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control"

// TestLinuxDelegationRequiresEnabledSubtreeControllers proves available controllers must also be enabled and subtree read failures name their path through RunWithHost's file-reader and fake Podman seams.
func TestLinuxDelegationRequiresEnabledSubtreeControllers(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, message string
		unreadable             bool
		status                 int
	}{
		{"pids-not-enabled", "cpu memory", "delegated cgroup pids controller is required", false, 1},
		{"all-enabled", "cpu memory pids", "host prerequisites satisfied", false, 0},
		{"subtree-unreadable", "", "read delegated cgroup controllers:", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linuxHost(t)
			if tc.unreadable {
				delete(f.files, linuxSubtreeControl)
			} else {
				f.files[linuxSubtreeControl] = tc.enabled
			}
			status, output, calls := f.run(t, "check")
			if status != tc.status || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%q; want status=%d message=%q", status, output, tc.status, tc.message)
			}
			if tc.unreadable && !strings.Contains(output, linuxSubtreeControl) {
				t.Fatalf("subtree read failure omitted path: %s", output)
			}
			noMutation(t, calls)
		})
	}
}

// TestCheckParsesInfoStdoutDespiteStderrWarning proves a successful JSON info response remains usable with a stderr warning through RunWithHost and fake Podman processes.
func TestCheckParsesInfoStdoutDespiteStderrWarning(t *testing.T) {
	f := linuxHost(t)
	info := f.responses["info --format json"]
	info.Stderr = "time=warning level=warning msg=\"logrus warning\"\n"
	f.responses["info --format json"] = info
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "host prerequisites satisfied") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	noMutation(t, calls)
}

// TestQueryFailurePreservesDiagnosticStreams proves nonzero queries keep their exit status and stdout/stderr diagnostics through RunWithHost and fake Podman processes.
func TestQueryFailurePreservesDiagnosticStreams(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    response
		messages []string
	}{
		{"stderr-only", response{Stderr: " stderr diagnostic \n", Exit: 125}, []string{"stderr diagnostic"}},
		{"both-streams", response{Output: "stdout diagnostic\n", Stderr: " stderr diagnostic \n", Exit: 125}, []string{"stdout diagnostic", "stderr diagnostic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["info --format json"] = tc.value
			status, output, calls := f.run(t, "check")
			if status != 1 || !strings.Contains(output, "podman info --format json: exit status 125:") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			for _, message := range tc.messages {
				if !strings.Contains(output, message) {
					t.Fatalf("missing %q from failure: %s", message, output)
				}
			}
			noMutation(t, calls)
		})
	}
}

func linuxHost(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	f := &fixture{fakes: testutil.Install(t, fakeBinary), files: map[string]string{
		"/etc/subuid": "tester:100000:65536\n", "/etc/subgid": "1000:100000:65536\n",
		"/proc/self/cgroup": "0::/user.slice/user-1000.slice/user@1000.service/app.slice/terminal.scope\n",
		"/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers": "cpu memory pids\n",
		linuxSubtreeControl: "cpu memory pids\n",
	}, responses: map[string]response{
		portInventory:        {Output: "[]"},
		"--version":          {Output: "podman version 4.4.0\n"},
		"info --format json": {Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`},
	}}
	for _, entry := range f.fakes.Env(os.Environ()) {
		key, value, _ := strings.Cut(entry, "=")
		if key == "PATH" || strings.HasPrefix(key, "TEST_") || key == "SANDBOXED_AGENTS_GROUP" {
			t.Setenv(key, value)
		}
	}
	data, err := os.ReadFile(fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"newuidmap", "newgidmap", "pasta", "ssh-keygen"} {
		if err := os.WriteFile(fakePath(f, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.host = cli.Host{Platform: "linux", Architecture: "amd64", PortAvailable: func(uint16) error { return nil }, CurrentUser: func() (*user.User, error) { return &user.User{Username: "tester", Uid: "1000"}, nil }, ReadFile: func(path string) ([]byte, error) {
		value, ok := f.files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(value), nil
	}}
	return f
}

func (f *fixture) run(t *testing.T, args ...string) (int, string, [][]string) {
	t.Helper()
	data, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	var output bytes.Buffer
	status := cli.RunWithHost(args, &output, &output, f.host)
	return status, output.String(), testutil.Invocations(t, f.fakes.PodmanLog)
}

func noMutation(t *testing.T, calls [][]string) {
	t.Helper()
	if err := mountBoundaryError(calls, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		args := call
		if len(args) >= 2 && args[0] == "--connection" {
			args = args[2:]
		}
		if len(args) == 4 && args[0] == "volume" && args[1] == "ls" && args[2] == "--format" && args[3] == "json" {
			continue
		}
		if len(args) == 3 && args[0] == "volume" && args[1] == "exists" {
			continue
		}
		if len(args) == 3 && args[0] == "volume" && args[1] == "inspect" {
			continue
		}
		for _, arg := range args {
			if arg == "build" || arg == "create" || arg == "start" || arg == "run" || arg == "volume" || arg == "rm" || arg == "rename" {
				t.Fatalf("unexpected mutation: %v", calls)
			}
		}
	}
}

// TestLinuxPreflightMissingPrerequisites proves each missing host requirement has its own failure through RunWithHost and fake Podman, before build or up mutates anything.
func TestLinuxPreflightMissingPrerequisites(t *testing.T) {
	cases := []struct {
		name, message string
		breakHost     func(*testing.T, *fixture)
	}{
		{"version", "Podman >= 4.4.0", func(t *testing.T, f *fixture) { f.responses["--version"] = response{Output: "podman version 4.3.1"} }},
		{"rootless", "rootless Podman is required", func(t *testing.T, f *fixture) {
			f.responses["info --format json"] = response{Output: `{"host":{"security":{"rootless":false},"cgroupVersion":"v2"}}`}
		}},
		{"newuidmap", "newuidmap is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "newuidmap")) }},
		{"newgidmap", "newgidmap is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "newgidmap")) }},
		{"pasta", "pasta is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "pasta")) }},
		{"ssh", "ssh is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh")) }},
		{"ssh-keygen", "ssh-keygen is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh-keygen")) }},
		{"subuid", "subordinate UID range", func(t *testing.T, f *fixture) { delete(f.files, "/etc/subuid") }},
		{"subgid", "subordinate GID range", func(t *testing.T, f *fixture) { f.files["/etc/subgid"] = "other:1:65536\n" }},
		{"cgroup-v2", "cgroups v2 is required", func(t *testing.T, f *fixture) {
			f.responses["info --format json"] = response{Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v1"}}`}
		}},
		{"cpu", "delegated cgroup cpu controller", func(t *testing.T, f *fixture) {
			f.files["/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"] = "memory pids"
		}},
		{"memory", "delegated cgroup memory controller", func(t *testing.T, f *fixture) {
			f.files["/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"] = "cpu pids"
		}},
		{"pids", "delegated cgroup pids controller", func(t *testing.T, f *fixture) {
			f.files["/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"] = "cpu memory"
		}},
		{"podman", "podman is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "podman")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, args := range [][]string{{"check"}, {"build"}, {"up", "demo"}} {
				t.Run(strings.Join(args, "-"), func(t *testing.T) {
					f := linuxHost(t)
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

// TestPreflightReportsAllMissingPrerequisites proves check reports independent missing requirements together through RunWithHost without host repair.
func TestPreflightReportsAllMissingPrerequisites(t *testing.T) {
	f := linuxHost(t)
	for _, name := range []string{"pasta", "ssh", "ssh-keygen"} {
		os.Remove(fakePath(f, name))
	}
	status, output, calls := f.run(t, "check")
	for _, name := range []string{"pasta", "ssh", "ssh-keygen"} {
		if !strings.Contains(output, name+" is required on PATH") {
			t.Errorf("missing %s: %s", name, output)
		}
	}
	if status != 1 {
		t.Fatalf("status=%d", status)
	}
	noMutation(t, calls)
}

// TestCheckSuccessIsReadOnly proves a complete Linux host passes check through RunWithHost with only Podman version and info queries.
func TestCheckSuccessIsReadOnly(t *testing.T) {
	f := linuxHost(t)
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "host prerequisites satisfied") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("SSH was executed: %v", calls)
	}
}

// TestPreflightUnusableQueriesFailClosed proves malformed or failed host queries refuse image and sandbox work through RunWithHost and fake Podman.
func TestPreflightUnusableQueriesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     response
	}{
		{"version-malformed", "--version", response{Output: "unknown"}},
		{"version-failed", "--version", response{Output: "podman version 9.0.0", Exit: 125}},
		{"info-malformed", "info --format json", response{Output: "{"}},
		{"info-failed", "info --format json", response{Exit: 125}},
		{"info-missing-fields", "info --format json", response{Output: "{}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linuxHost(t)
			f.responses[tc.key] = tc.value
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "step 2:") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

func fakePath(f *fixture, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(f.fakes.Dir, name)
}

// TestLinuxLoginSessionUsesDelegatedUserManager proves a login scope checks its user's delegated manager rather than the session leaf through RunWithHost.
func TestLinuxLoginSessionUsesDelegatedUserManager(t *testing.T) {
	f := linuxHost(t)
	f.files["/proc/self/cgroup"] = "0::/user.slice/user-1000.slice/session-1.scope\n"
	status, output, calls := f.run(t, "check")
	if status != 0 {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestLinuxHostRootControllersAreNotDelegation proves controllers at the host root do not satisfy user delegation through RunWithHost.
func TestLinuxHostRootControllersAreNotDelegation(t *testing.T) {
	f := linuxHost(t)
	f.files["/proc/self/cgroup"] = "0::/\n"
	f.files["/sys/fs/cgroup/cgroup.controllers"] = "cpu memory pids"
	status, output, calls := f.run(t, "check")
	if status != 1 || !strings.Contains(output, "delegated cgroup") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestCheckNamedSandboxSkipsPreflight proves check NAME inspects sandbox state without a host preflight through RunWithHost and fake Podman.
func TestCheckNamedSandboxSkipsPreflight(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	f.responses["--version"] = response{Output: "unusable Podman", Exit: 125}
	f.responses["info --format json"] = response{Output: "unusable Podman", Exit: 125}
	status, output, calls := f.run(t, "check", "demo")
	if status != 1 || !strings.Contains(output, "does not exist in this controller group") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	for _, call := range calls {
		if call[0] == "--version" || call[0] == "info" {
			t.Fatalf("named sandbox check ran preflight: %v", calls)
		}
	}
	noMutation(t, calls)
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestLinuxFailedInfoReportsUnverifiedPrerequisites proves a failed info query retains its raw cause and separately reports rootless and cgroup checks through RunWithHost and fake Podman.
func TestLinuxFailedInfoReportsUnverifiedPrerequisites(t *testing.T) {
	f := linuxHost(t)
	f.responses["info --format json"] = response{Output: "engine info unavailable", Exit: 125}
	status, output, calls := f.run(t, "check")
	if !strings.HasPrefix(output, "step 2: podman info --format json: exit status 125: engine info unavailable\n") {
		t.Errorf("raw info failure missing: %s", output)
	}
	for _, message := range []string{"rootless Podman is required (cannot verify:", "cgroups v2 is required (cannot verify:"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 || len(calls) != 2 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}

// TestLinuxMissingPodmanReportsUnverifiedPrerequisites proves a missing client separately reports unverifiable version, rootless, and cgroup requirements without process calls through RunWithHost.
func TestLinuxMissingPodmanReportsUnverifiedPrerequisites(t *testing.T) {
	f := linuxHost(t)
	if err := os.Remove(fakePath(f, "podman")); err != nil {
		t.Fatal(err)
	}
	status, output, calls := f.run(t, "check")
	if !strings.HasPrefix(output, "step 2: podman is required on PATH\n") {
		t.Errorf("missing PATH diagnostic: %s", output)
	}
	for _, message := range []string{"Podman >= 4.4.0 is required (cannot verify:", "rootless Podman is required (cannot verify:", "cgroups v2 is required (cannot verify:"} {
		if !strings.Contains(output, message) {
			t.Errorf("missing %q: %s", message, output)
		}
	}
	if status != 1 || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}
