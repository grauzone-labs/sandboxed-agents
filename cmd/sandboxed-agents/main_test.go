package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

var executable string
var fakeExecutable string
var releaseExecutable string

// nestedGuardEnv carries the PID of the process that started the nested run.
const nestedGuardEnv = "TEST_OFFLINE_GUARD_NESTED"

// TestMain prepares development, release, and fake binaries for the built CLI seam, reuses them in nested runs, and removes its temporary build directory after testing.
func TestMain(m *testing.M) {
	// Nested test runs reuse parent-built binaries with an isolated PATH.
	executable = os.Getenv("TEST_SANDBOXED_AGENTS_EXECUTABLE")
	fakeExecutable = os.Getenv("TEST_FAKE_EXECUTABLE")
	releaseExecutable = os.Getenv("TEST_RELEASE_EXECUTABLE")
	if executable != "" && fakeExecutable != "" && releaseExecutable != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "sandboxed-agents-cli-")
	if err != nil {
		panic(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	executable = filepath.Join(dir, "sandboxed-agents"+suffix)
	build := exec.Command("go", "build", "-o", executable, ".")
	if output, err := build.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	releaseExecutable = filepath.Join(dir, "release"+suffix)
	versionSymbol, err := release.VersionSymbol(os.DirFS("../.."))
	if err != nil {
		panic(err)
	}
	releaseBuild := exec.Command("go", "build", "-ldflags", "-X "+versionSymbol+"="+releasetest.ReleaseVersion, "-o", releaseExecutable, ".")
	if output, err := releaseBuild.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	fakeExecutable = filepath.Join(dir, "fake"+suffix)
	fakeBuild := exec.Command("go", "build", "-o", fakeExecutable, "../../internal/testutil/testdata/fake.go")
	if output, err := fakeBuild.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// TestVersionReportsInjectedRelease proves that a linker-injected release tag and the asset hash are printed through the built CLI binary without external calls.
func TestVersionReportsInjectedRelease(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	command := exec.Command(releaseExecutable, "version")
	command.Env = fakes.Env(os.Environ())
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releasetest.ReleaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("version: error=%v output=%q; want %q", err, output, want)
	}
	assertNoExternalCalls(t, fakes)
}

// TestUnknownCommandIsUsageError proves that an unknown command exits unsuccessfully with a usage diagnostic through the built CLI binary without Podman or SSH calls.
func TestUnknownCommandIsUsageError(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	command := exec.Command(executable, "unknown")
	command.Env = fakes.Env(os.Environ())
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "usage:") {
		t.Fatalf("unknown command: error=%v output=%q; want usage error", err, output)
	}
	assertNoExternalCalls(t, fakes)
}

// TestOfflineSuiteDoesNotStartLiveTests proves that the whole nested offline suite skips the live test and makes no Podman or SSH calls through the fake process harness.
// The guard checks calls recorded in its own fake logs inherited by the subprocess, so a test installing its own fakes with testutil.Install must assert them itself with assertNoExternalCalls.
func TestOfflineSuiteDoesNotStartLiveTests(t *testing.T) {
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("nested offline run of the guard; the outer guard run covers it")
	}
	fakes := testutil.Install(t, fakeExecutable)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(testExecutable, "-test.v", "-test.run=.")
	child.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{
		"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable, "TEST_RELEASE_EXECUTABLE": releaseExecutable,
		nestedGuardEnv: strconv.Itoa(os.Getpid()),
	})
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("offline test run: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "live test body selected") || !strings.Contains(string(output), "--- SKIP: TestLivePodmanVersion") {
		t.Fatalf("live test ran without opt-in: %s", output)
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveOptInSelectsOnlyExplicitOne proves that only an opt-in value of "1" runs the live test and invokes Podman version through the fake process harness.
func TestLiveOptInSelectsOnlyExplicitOne(t *testing.T) {
	for _, value := range []string{"0", "true", "1"} {
		t.Run(value, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			testExecutable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(testExecutable, "-test.v", "-test.run=^TestLivePodmanVersion$")
			child.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{
				testutil.LiveTestsEnv:              value,
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable, "TEST_RELEASE_EXECUTABLE": releaseExecutable,
			})
			output, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("live selection: %v\n%s", err, output)
			}
			selected := strings.Contains(string(output), "live test body selected")
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if value == "1" {
				if !selected || len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != "version" {
					t.Fatalf("opt-in: selected=%v calls=%v output=%s", selected, calls, output)
				}
			} else if selected || len(calls) != 0 {
				t.Fatalf("unexpected live selection: %s calls=%v", output, calls)
			}
		})
	}
}

// TestLiveOptInFailsWhenPodmanIsMissingOrUnusable proves that an opted-in live test fails with a Podman diagnostic for a missing or failing program through the fake process harness.
func TestLiveOptInFailsWhenPodmanIsMissingOrUnusable(t *testing.T) {
	for _, availability := range []string{"missing", "unusable"} {
		t.Run(availability, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			testExecutable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			environment := testutil.Env(fakes.Env(os.Environ()), map[string]string{
				testutil.LiveTestsEnv:              "1",
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable, "TEST_RELEASE_EXECUTABLE": releaseExecutable,
				"TEST_PODMAN_EXIT": "23", "TEST_PODMAN_OUTPUT": "scripted Podman failure\n",
			})
			if availability == "missing" {
				environment = testutil.Env(environment, map[string]string{"PATH": t.TempDir()})
			}
			child := exec.Command(testExecutable, "-test.v", "-test.run=^TestLivePodmanVersion$")
			child.Env = environment
			output, err := child.CombinedOutput()
			if status := exitStatus(t, err); status != 1 || !strings.Contains(string(output), "--- FAIL: TestLivePodmanVersion") || !strings.Contains(string(output), "Podman") {
				t.Fatalf("opted-in %s Podman: status=%d output=%s; want failure naming Podman", availability, status, output)
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if availability == "missing" {
				if len(calls) != 0 {
					t.Fatalf("missing Podman calls: %v", calls)
				}
			} else {
				if !reflect.DeepEqual(calls, [][]string{{"version"}}) || !strings.Contains(string(output), "exit status 23") || !strings.Contains(string(output), "scripted Podman failure") {
					t.Fatalf("unusable Podman: calls=%v output=%s", calls, output)
				}
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected SSH calls: %v", calls)
			}
		})
	}
}

// TestCommandGrammar proves command forms produce their expected success, preflight-failure, not-implemented, or usage status through the built CLI binary.
// Before a deferred stub, only --version, info --format json, or machine list --format json may reach Podman; SSH never runs.
func TestCommandGrammar(t *testing.T) {
	cases := []struct {
		args   []string
		status int
	}{
		{[]string{"version"}, 0}, {[]string{"list"}, 1}, {[]string{"check"}, 1},
		{[]string{"build", "--with", "none"}, 1}, {[]string{"up", "demo"}, 1},
		{[]string{"start", "demo", "--ssh-config"}, 1}, {[]string{"stop", "demo", "--force"}, 1},
		{[]string{"restart", "demo", "--force"}, 1}, {[]string{"remove", "demo", "--volumes", "--force"}, 1},
		{[]string{"update", "demo", "--with", "none", "--force"}, 1}, {[]string{"update", "--all", "--force"}, 1},
		{[]string{"update", "--force", "--all"}, 1},
		{[]string{"shell", "demo"}, 1}, {[]string{"ssh-config", "demo", "--install"}, 1},
		{[]string{"ssh-config", "demo", "--remove"}, 1}, {[]string{"fingerprint", "demo"}, 1}, {[]string{"check", "demo"}, 1},
		{[]string{"agents", "enable", "demo", "codex", "--version", "1.2", "--force"}, 2},
		{[]string{"agents", "disable", "demo", "codex", "--force"}, 2},
		{[]string{"agents", "update", "demo", "codex", "--unpin", "--force"}, 2},
		{[]string{"agents", "login", "demo", "codex", "browser"}, 2},
		{[]string{"agents", "status", "demo", "codex"}, 2},
		{[]string{"agents", "run", "demo", "codex", "--unknown-to-host", "hello"}, 2},
		{[]string{"agents", "session", "demo", "codex", "--stop"}, 2},
		{[]string{"integrations", "login", "demo", "github", "browser"}, 2},
		{[]string{"integrations", "login", "demo", "azure"}, 1},
		{[]string{"integrations", "login", "demo", "azdo", "--organization", "https://example.com/org"}, 1},
		{[]string{"integrations", "config", "demo", "git", "identity", "--name", "A Person", "--email", "a@example.com"}, 1},
		{[]string{"integrations", "config", "demo", "git", "credentials"}, 1},
		{nil, 2}, {[]string{"demo", "up"}, 2}, {[]string{"agents", "unknown", "demo", "codex"}, 2},
		{[]string{"up"}, 2}, {[]string{"start", "--ssh-config", "demo"}, 2},
		{[]string{"stop", "demo", "--volumes"}, 2}, {[]string{"build", "demo"}, 2},
		{[]string{"update", "demo", "--all"}, 2}, {[]string{"update", "--all", "--with", "none"}, 2},
		{[]string{"ssh-config", "demo", "--install", "--remove"}, 2},
		{[]string{"integrations", "login", "demo", "unknown"}, 2},
		{[]string{"integrations", "login", "demo", "azdo"}, 2},
		{[]string{"integrations", "config", "demo", "git", "unknown"}, 2},
	}
	fakes := testutil.Install(t, fakeExecutable)
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			caseFakes := fakes
			lifecycle := len(tc.args) > 0 && (tc.args[0] == "start" || tc.args[0] == "stop" || tc.args[0] == "restart" || tc.args[0] == "remove")
			if tc.status == 2 || lifecycle {
				caseFakes = testutil.Install(t, fakeExecutable)
			}
			command := exec.Command(executable, tc.args...)
			command.Env = caseFakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			status := exitStatus(t, err)
			if status != tc.status {
				t.Fatalf("status=%d want=%d output=%q", status, tc.status, output)
			}
			if (tc.status == 2) != strings.Contains(string(output), "usage:") {
				t.Fatalf("unexpected diagnostic: %s", output)
			}
			if tc.status == 2 {
				assertNoExternalCalls(t, caseFakes)
			}
			if lifecycle && tc.status != 2 {
				calls := testutil.Invocations(t, caseFakes.PodmanLog)
				for _, call := range calls {
					if len(call) < 2 || call[0] != "container" || (call[1] != "exists" && call[1] != "inspect") {
						t.Fatalf("unexpected lifecycle grammar call: %v", calls)
					}
				}
			}
		})
	}
	assertOnlyPreflightQueries(t, fakes)
}

func exitStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	t.Fatal(err)
	return -1
}

func assertNoExternalCalls(t *testing.T, fakes testutil.Fakes) {
	t.Helper()
	for _, path := range []string{fakes.PodmanLog, fakes.SSHLog} {
		if calls := testutil.Invocations(t, path); len(calls) != 0 {
			t.Fatalf("unexpected external calls: %v", calls)
		}
	}
}

// TestAgentAndToolchainSelectionsUseDeliveredCatalogs proves unknown selections fail usage and "none" reaches preflight before the deferred up lifecycle through the built CLI binary.
// Unknown selections make no external calls; none permits only Podman's preflight queries and never runs SSH.
func TestAgentAndToolchainSelectionsUseDeliveredCatalogs(t *testing.T) {
	cases := []struct {
		option, value string
		status        int
		diagnostic    string
	}{
		{"agents", "bogus", 2, `unknown agent "bogus": no valid names are available yet`},
		{"with", "bogus", 2, `unknown toolchain "bogus": no valid names are available yet`},
		{"with", "none", 1, "step 2:"},
	}
	for _, tc := range cases {
		t.Run(tc.option+"="+tc.value, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, "up", "demo", "--"+tc.option, tc.value)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status || !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("status=%d output=%q; want status=%d diagnostic=%q", status, output, tc.status, tc.diagnostic)
			}
			if tc.status == 2 {
				assertNoExternalCalls(t, fakes)
			} else {
				assertOnlyPreflightQueries(t, fakes)
			}
		})
	}
}

// TestAgentCommandsRejectUnknownAgentsBeforeExternalCalls proves that every agent command rejects an unknown agent at step 1 through the built CLI binary before Podman or SSH runs.
func TestAgentCommandsRejectUnknownAgentsBeforeExternalCalls(t *testing.T) {
	for _, verb := range []string{"enable", "disable", "update", "login", "status", "run", "session"} {
		t.Run(verb, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, "agents", verb, "demo", "bogus")
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			want := "step 1: unknown agent \"bogus\": no valid names are available yet\n"
			if status := exitStatus(t, err); status != 2 || !strings.HasPrefix(string(output), want) {
				t.Errorf("status=%d output=%q; want status=2 and first line=%q", status, output, want)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestLoginWorkflowsAreValidatedBeforeExternalCalls proves that invalid login requests are usage errors and an omitted GitHub workflow reaches the stub through the built CLI binary without external calls.
func TestLoginWorkflowsAreValidatedBeforeExternalCalls(t *testing.T) {
	cases := []struct {
		args       []string
		status     int
		diagnostic string
	}{
		{[]string{"integrations", "login", "demo", "github", "bogusflow"}, 2, `unknown workflow "bogusflow": no valid names are available yet`},
		{[]string{"integrations", "login", "demo", "azure", "bogusflow"}, 2, `unknown workflow "bogusflow": no valid names are available yet`},
		{[]string{"integrations", "login", "demo", "azdo", "bogusflow", "--organization", "https://example.com/org"}, 2, `unknown workflow "bogusflow": no valid names are available yet`},
		{[]string{"integrations", "login", "demo", "github"}, 1, "integrations login: not implemented\n"},
		{[]string{"agents", "login", "demo", "codex", "bogusflow"}, 2, ""},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, tc.args...)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status || !strings.Contains(string(output), tc.diagnostic) {
				t.Errorf("status=%d output=%q; want status=%d diagnostic=%q", status, output, tc.status, tc.diagnostic)
			}
			if tc.status == 1 && strings.Contains(string(output), "unknown") {
				t.Errorf("omitted workflow rejected as unknown: %s", output)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestControllerGroupAndSandboxNames proves valid names reach preflight and invalid controller group or sandbox names fail usage through the built CLI binary.
// Only --version, info --format json, or machine list --format json may reach Podman; SSH never runs.
func TestControllerGroupAndSandboxNames(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	cases := []struct {
		group, name string
		status      int
	}{
		{"", "Demo_01.a-b", 2}, {"default", "Demo_01.a-b", 1}, {"team-2", "up", 1}, {"0", "con", 1},
		{"Bad", "demo", 2}, {"bad.group", "demo", 2}, {"-bad", "demo", 2}, {"bad_group", "demo", 2},
		{"default", "-bad", 2}, {"default", "bad/name", 2}, {"default", "bad name", 2}, {"default", ".bad", 2}, {"default", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.group+"/"+tc.name, func(t *testing.T) {
			caseFakes := fakes
			if tc.status == 2 {
				caseFakes = testutil.Install(t, fakeExecutable)
			}
			command := exec.Command(executable, "up", tc.name)
			command.Env = testutil.Env(caseFakes.Env(os.Environ()), map[string]string{"SANDBOXED_AGENTS_GROUP": tc.group})
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d want=%d output=%s", status, tc.status, output)
			}
			if tc.status == 2 && !strings.Contains(string(output), "usage:") {
				t.Fatalf("missing usage diagnostic: %s", output)
			}
			if tc.group == "" && !strings.HasPrefix(string(output), "step 1: invalid controller group \"\"\n") {
				t.Fatalf("empty controller group diagnostic: %s", output)
			}
			if tc.status == 2 {
				assertNoExternalCalls(t, caseFakes)
			}
		})
	}
	assertOnlyPreflightQueries(t, fakes)
}

// TestUnsetControllerGroupUsesDefault proves an unset controller group uses default and reaches preflight before the deferred up lifecycle through the built CLI binary.
// Only --version, info --format json, or machine list --format json may reach Podman; SSH never runs.
func TestUnsetControllerGroupUsesDefault(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	command := exec.Command(executable, "up", "demo")
	for _, entry := range fakes.Env(os.Environ()) {
		if !strings.HasPrefix(entry, "SANDBOXED_AGENTS_GROUP=") {
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.CombinedOutput()
	if status := exitStatus(t, err); status != 1 || !strings.Contains(string(output), "step 2:") {
		t.Fatalf("unset controller group: status=%d output=%q; want status=1 and preflight failure", status, output)
	}
	assertOnlyPreflightQueries(t, fakes)
}

// TestVersionIgnoresInvalidControllerGroup proves that version succeeds with its expected output despite an invalid or empty controller group through the built CLI binary without external calls.
func TestVersionIgnoresInvalidControllerGroup(t *testing.T) {
	for _, group := range []string{"Bad", ""} {
		t.Run(group, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, "version")
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"SANDBOXED_AGENTS_GROUP": group})
			output, err := command.CombinedOutput()
			want := releasetest.ExpectedVersion(t, "development")
			if status := exitStatus(t, err); status != 0 || string(output) != want {
				t.Fatalf("version: status=%d output=%q; want status=0 and version", status, output)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestResourceOptionGrammar proves valid resource forms reach preflight before the deferred up lifecycle, while malformed or out-of-range values fail usage through the built CLI binary.
// Only --version, info --format json, or machine list --format json may reach Podman; SSH never runs.
func TestResourceOptionGrammar(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	cases := []struct {
		option, value string
		status        int
	}{
		{"memory", "6m", 1}, {"memory", "6M", 1}, {"memory", "8g", 1}, {"memory", "6291456", 1},
		{"memory", "9223372036854775807", 1}, {"memory", "8388608t", 2}, {"memory", "9223372036854775808", 2},
		{"memory", "1", 2}, {"memory", "5m", 2}, {"memory", "0", 2}, {"memory", "-1", 2}, {"memory", "+8g", 2},
		{"memory", "8gb", 2}, {"memory", "8.0g", 2}, {"memory", "1e9", 2}, {"memory", " 8g", 2}, {"memory", "", 2},
		{"shm-size", "1", 1}, {"shm-size", "1K", 1}, {"shm-size", "1M", 1}, {"shm-size", "1G", 1}, {"shm-size", "1T", 1},
		{"shm-size", "9223372036854775807", 1}, {"shm-size", "0k", 2}, {"shm-size", "1.5g", 2},
		{"shm-size", "9007199254740992k", 2}, {"shm-size", "99999999999999999999999999999999", 2},
		{"cpus", "4", 1}, {"cpus", "0.5", 1}, {"cpus", "0", 2}, {"cpus", "-1", 2}, {"cpus", "+1", 2},
		{"cpus", "1e3", 2}, {"cpus", "NaN", 2}, {"cpus", "Inf", 2}, {"cpus", "0.0", 2},
		{"cpus", "1.2345", 2}, {"cpus", "9223372036.855", 2}, {"cpus", "99999999999999999999", 2},
		{"cpus", "9223372036.854", 1}, {"cpus", "1.234", 1}, {"cpus", "", 2},
		{"cpus", "1.", 2}, {"cpus", ".5", 2}, {"cpus", "0.001", 1},
		{"pids-limit", "2048", 1}, {"pids-limit", "1", 1}, {"pids-limit", "0", 2}, {"pids-limit", "-1", 2},
		{"pids-limit", "+1", 2}, {"pids-limit", "1.5", 2}, {"pids-limit", "1e3", 2}, {"pids-limit", "9223372036854775808", 2},
		{"port", "22", 1}, {"port", "65535", 1}, {"port", "0", 2}, {"port", "65536", 2}, {"port", "+22", 2},
	}
	for _, tc := range cases {
		t.Run(tc.option+"="+tc.value, func(t *testing.T) {
			caseFakes := fakes
			if tc.status == 2 {
				caseFakes = testutil.Install(t, fakeExecutable)
			}
			command := exec.Command(executable, "up", "demo", "--"+tc.option, tc.value)
			command.Env = caseFakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d want=%d output=%s", status, tc.status, output)
			}
			if tc.option == "cpus" && tc.status == 1 && !strings.Contains(string(output), "step 2:") {
				t.Fatalf("output=%q; want the preflight path", output)
			}
			if tc.status == 2 {
				assertNoExternalCalls(t, caseFakes)
			}
		})
	}
	assertOnlyPreflightQueries(t, fakes)
}

// TestWorkspacePathsReachPreflightOrFailBeforePodman proves a workspace directory reaches preflight before the deferred up lifecycle, while invalid or protected paths fail usage through the built CLI binary.
// Only --version, info --format json, or machine list --format json may reach Podman; SSH never runs.
func TestWorkspacePathsReachPreflightOrFailBeforePodman(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	home := t.TempDir()
	workspace := t.TempDir()
	file := filepath.Join(workspace, "file")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(home, ".ssh")
	if err := os.Mkdir(ssh, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "ssh-alias")
	if err := os.Symlink(ssh, alias); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	cases := []struct {
		path   string
		status int
	}{
		{workspace, 1}, {filepath.Join(workspace, "missing"), 2}, {file, 2},
		{ssh, 2}, {home, 2}, {alias, 2}, {filepath.Dir(executable), 2},
		{filepath.Join(state, "sandboxed-agents"), 2},
	}
	if err := os.Mkdir(filepath.Join(state, "sandboxed-agents"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			caseFakes := fakes
			if tc.status == 2 {
				caseFakes = testutil.Install(t, fakeExecutable)
			}
			command := exec.Command(executable, "up", "demo", tc.path, "--with", "none")
			command.Env = testutil.Env(caseFakes.Env(os.Environ()), map[string]string{"HOME": home, "USERPROFILE": home, "XDG_STATE_HOME": state, "LOCALAPPDATA": state})
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d want=%d output=%s", status, tc.status, output)
			}
			if tc.status == 2 {
				assertNoExternalCalls(t, caseFakes)
			}
		})
	}
	assertOnlyPreflightQueries(t, fakes)
}

// TestExternalFakesRecordArgvAndScriptResponses proves that fake Podman and SSH preserve argument boundaries and return scripted output and exit status through the fake process harness.
func TestExternalFakesRecordArgvAndScriptResponses(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	for _, program := range []string{"podman", "ssh"} {
		t.Run(program, func(t *testing.T) {
			path := filepath.Join(fakes.Dir, program)
			if runtime.GOOS == "windows" {
				path += ".exe"
			}
			command := exec.Command(path, "two words", "a\tb", "quote\"", "", "--flag")
			prefix := "TEST_" + strings.ToUpper(program)
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{prefix + "_OUTPUT": "scripted output\n", prefix + "_EXIT": "23"})
			output, err := command.CombinedOutput()
			if exitStatus(t, err) != 23 || string(output) != "scripted output\n" {
				t.Fatalf("error=%v output=%q", err, output)
			}
			log := fakes.PodmanLog
			if program == "ssh" {
				log = fakes.SSHLog
			}
			want := [][]string{{"two words", "a\tb", "quote\"", "", "--flag"}}
			if calls := testutil.Invocations(t, log); !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%q want=%q", calls, want)
			}
		})
	}
}

// TestMalformedArgumentsRemainUsageErrors proves that malformed command arguments produce step 1 usage errors through the built CLI binary without Podman or SSH calls.
func TestMalformedArgumentsRemainUsageErrors(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	cases := [][]string{
		{"agents enable", "demo", "codex"},
		{"integrations login", "demo", "github"},
		{"agents", "run", "", "codex"},
		{"up", "demo", "--with", "go,"},
		{"up", "demo", "--agents", ",codex"},
		{"build", "--with", "none,go"},
		{"up", "demo", "--force"},
		{"version", "--unknown"},
		{"start", "demo", "--ssh-config=true"},
		{"up", "demo", "--memory"},
		{"up", "demo", "--cpus", "2", "--cpus", "4"},
		{"agents", "status", "demo", "codex", "--force"},
		{"integrations", "login", "demo", "github", "--organization", "https://example.com"},
		{"integrations", "login", "demo", "azdo", "browser", "--organization", "https://example.com"},
		{"integrations", "config", "demo", "git", "credentials", "--email", "e@example.com"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := exec.Command(executable, args...)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if exitStatus(t, err) != 2 || !strings.Contains(string(output), "step 1:") || !strings.Contains(string(output), "usage:") {
				t.Fatalf("error=%v output=%s; want step 1 usage error", err, output)
			}
		})
	}
	assertNoExternalCalls(t, fakes)
}

func assertOnlyPreflightQueries(t *testing.T, fakes testutil.Fakes) {
	t.Helper()
	for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
		if !reflect.DeepEqual(call, []string{"--version"}) && !reflect.DeepEqual(call, []string{"info", "--format", "json"}) && !reflect.DeepEqual(call, []string{"machine", "list", "--format", "json"}) {
			t.Fatalf("unexpected call beyond preflight: %v", call)
		}
	}
	if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestMissingWorkspaceFailsBeforeUnusablePreflight proves a missing workspace remains a path usage error without Podman or SSH calls through the built CLI binary and failing process fakes.
func TestMissingWorkspaceFailsBeforeUnusablePreflight(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	missing := filepath.Join(t.TempDir(), "missing")
	command := exec.Command(executable, "up", "demo", missing)
	command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"TEST_PODMAN_OUTPUT": "unexpected preflight sentinel", "TEST_PODMAN_EXIT": "125", "TEST_SSH_EXIT": "125"})
	output, err := command.CombinedOutput()
	if status := exitStatus(t, err); status != 2 || !strings.HasPrefix(string(output), "step 1: workspace ") || !strings.Contains(string(output), strconv.Quote(missing)) || strings.Contains(string(output), "unexpected preflight sentinel") {
		t.Fatalf("status=%d output=%q; want missing workspace usage error before preflight", status, output)
	}
	assertNoExternalCalls(t, fakes)
}

// lifecycleResponse scripts only the external Podman boundary.
type lifecycleResponse struct {
	Output             string
	Exit               int
	ReadyFile, WaitFor string
}

func lifecycleHarness(t *testing.T, responses map[string]lifecycleResponse) testutil.Fakes {
	t.Helper()
	fakes := testutil.Install(t, fakeExecutable)
	state := t.TempDir()
	data, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	environment := testutil.Env(fakes.Env(os.Environ()), map[string]string{
		"XDG_STATE_HOME": state, "LOCALAPPDATA": state, "TEST_PODMAN_RESPONSES": string(data),
	})
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			t.Setenv(key, value)
		}
	}
	return fakes
}

func lifecycleRun(args ...string) (int, string) {
	var output bytes.Buffer
	status := cli.RunWithHost(args, &output, &output, cli.Host{Platform: "linux"})
	return status, output.String()
}

// TestLifecycleLockRefusesBeforeSandboxLookup proves a competing command refuses immediately before any sandbox lookup through RunWithHost and fake Podman.
func TestLifecycleLockRefusesBeforeSandboxLookup(t *testing.T) {
	rendezvous := t.TempDir()
	ready, release := filepath.Join(rendezvous, "ready"), filepath.Join(rendezvous, "release")
	fakes := lifecycleHarness(t, map[string]lifecycleResponse{
		"container exists sandboxed-agents.default.demo": {Exit: 1, ReadyFile: ready, WaitFor: release},
		"--version":          {Output: "podman version 4.4.0"},
		"info --format json": {Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`},
	})
	done := make(chan struct{})
	go func() { defer close(done); lifecycleRun("start", "demo") }()
	defer func() { os.WriteFile(release, nil, 0600); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first command did not reach lookup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	host := lifecycleLinuxHost(t, fakes)
	for _, verb := range []string{"start", "stop", "restart", "remove", "up"} {
		start := time.Now()
		var out bytes.Buffer
		status := cli.RunWithHost([]string{verb, "demo"}, &out, &out, host)
		output := out.String()
		if status == 0 || !strings.Contains(output, "demo") || !strings.Contains(output, "another lifecycle command is in progress") || !strings.Contains(output, "retry") || time.Since(start) > time.Second {
			t.Errorf("%s: status=%d output=%s", verb, status, output)
		}
	}
	calls := testutil.Invocations(t, fakes.PodmanLog)
	want := [][]string{{"container", "exists", "sandboxed-agents.default.demo"}, {"--version"}, {"info", "--format", "json"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
	for _, args := range [][]string{{"start", "bad/name"}, {"up", "demo", filepath.Join(rendezvous, "missing")}} {
		status, output := lifecycleRun(args...)
		if status != 2 || !strings.Contains(output, "step 1:") {
			t.Fatalf("usage/path before lock: status=%d output=%s", status, output)
		}
	}
	// A failing preflight must win over the lock refusal.
	status, output := lifecycleRun("up", "demo")
	if status != 1 || !strings.Contains(output, "step 2:") || strings.Contains(output, "step 3:") {
		t.Fatalf("preflight before lock: status=%d output=%s", status, output)
	}

}

func lifecycleLinuxHost(t *testing.T, fakes testutil.Fakes) cli.Host {
	t.Helper()
	data, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"newuidmap", "newgidmap", "pasta", "ssh-keygen"} {
		if err := os.WriteFile(filepath.Join(fakes.Dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return cli.Host{Platform: "linux", Architecture: "amd64", CurrentUser: func() (*user.User, error) { return &user.User{Username: "tester", Uid: "1000"}, nil }, ReadFile: func(path string) ([]byte, error) {
		switch path {
		case "/etc/subuid", "/etc/subgid":
			return []byte("tester:100000:65536"), nil
		case "/proc/self/cgroup":
			return []byte("0::/user.slice/user-1000.slice/user@1000.service"), nil
		default:
			return []byte("cpu memory pids"), nil
		}
	}}
}

// TestLifecycleLocksAreEmptyPersistentAndRejectNonregularPaths proves failed commands retain empty hashed lock files and refuse nonregular paths before lookup through RunWithHost.
func TestLifecycleLocksAreEmptyPersistentAndRejectNonregularPaths(t *testing.T) {
	fakes := lifecycleHarness(t, map[string]lifecycleResponse{"container exists *": {Exit: 1}})
	for _, name := range []string{"demo", "Demo", "con"} {
		lifecycleRun("start", name)
	}
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "group-default", "locks")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("lock files=%v", files)
	}
	for _, file := range files {
		if len(file.Name()) != 64 || strings.Trim(file.Name(), "0123456789abcdef") != "" {
			t.Fatalf("lock filename=%q", file.Name())
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			t.Fatalf("lock info=%v error=%v", info, err)
		}
	}
	// sha256("sandboxed-agents.default.demo"), fixed independently for ADR-0007.
	path := filepath.Join(dir, "9a4080e774a647b4e19f0a6c7f45385183ffd91df8d99b0dcd9cdae7ea2d401c")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	before := testutil.Invocations(t, fakes.PodmanLog)
	status, output := lifecycleRun("start", "demo")
	if status == 0 || !strings.Contains(output, "not a regular file") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	if after := testutil.Invocations(t, fakes.PodmanLog); !reflect.DeepEqual(before, after) {
		t.Fatalf("nonregular lock made lookup: %v", after)
	}
}

func ownedLifecycleResponses(running bool) map[string]lifecycleResponse {
	return map[string]lifecycleResponse{
		"container exists sandboxed-agents.default.demo":                                                 {},
		"container inspect sandboxed-agents.default.demo":                                                {Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{"Running":` + strconv.FormatBool(running) + `}}]`},
		"container exists sandboxed-agents-backup.default.demo":                                          {Exit: 1},
		"volume exists sandboxed-agents.default.demo.workspace":                                          {Exit: 1},
		"volume exists sandboxed-agents.default.demo.home":                                               {Exit: 1},
		"volume exists sandboxed-agents.default.demo.ssh":                                                {Exit: 1},
		"exec --user=0:0 sandboxed-agents.default.demo /usr/local/bin/sandboxed-agents-manager sessions": {},
		"start sandboxed-agents.default.demo":                                                            {},
		"stop sandboxed-agents.default.demo":                                                             {},
		"restart sandboxed-agents.default.demo":                                                          {},
	}
}

func lifecycleMutations(calls [][]string) [][]string {
	var mutations [][]string
	for _, call := range calls {
		if len(call) > 0 && (call[0] == "start" || call[0] == "stop" || call[0] == "restart" || call[0] == "rm") {
			mutations = append(mutations, call)
		}
	}
	return mutations
}

// TestLifecycleStartChangesOnlyStoppedSandbox proves start is idempotent for a running sandbox and starts a stopped sandbox without preflight through RunWithHost.
func TestLifecycleStartChangesOnlyStoppedSandbox(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			fakes := lifecycleHarness(t, ownedLifecycleResponses(running))
			status, output := lifecycleRun("start", "demo")
			if status != 0 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			var want [][]string
			if !running {
				want = [][]string{{"start", "sandboxed-agents.default.demo"}}
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			if got := lifecycleMutations(calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("mutations=%v want=%v", got, want)
			}
			for _, call := range calls {
				if call[0] == "--version" || call[0] == "info" || call[0] == "exec" {
					t.Fatalf("unexpected prerequisite/session query: %v", calls)
				}
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("SSH calls=%v", calls)
			}
		})
	}
}

// TestLifecycleStopAndRestartPreserveRequestedState proves stop on stopped is a no-op, restart on stopped starts, and running transitions use only their Podman action through RunWithHost.
func TestLifecycleStopAndRestartPreserveRequestedState(t *testing.T) {
	for _, tc := range []struct {
		verb    string
		running bool
		action  string
	}{{"stop", false, ""}, {"stop", true, "stop"}, {"restart", false, "start"}, {"restart", true, "restart"}} {
		t.Run(tc.verb+strconv.FormatBool(tc.running), func(t *testing.T) {
			fakes := lifecycleHarness(t, ownedLifecycleResponses(tc.running))
			status, output := lifecycleRun(tc.verb, "demo")
			if status != 0 {
				t.Fatalf("status=%d output=%s", status, output)
			}
			var want [][]string
			if tc.action != "" {
				want = [][]string{{tc.action, "sandboxed-agents.default.demo"}}
			}
			if calls := testutil.Invocations(t, fakes.PodmanLog); !reflect.DeepEqual(lifecycleMutations(calls), want) {
				t.Fatalf("calls=%v want mutations=%v", calls, want)
			}
			if calls := testutil.Invocations(t, fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("SSH calls=%v", calls)
			}
		})
	}
}

// TestLifecycleRefusesEveryOwnerConflictBeforeMutation proves container, volume, and backup owner labels authorize lifecycle changes and refusals name Podman objects through RunWithHost.
func TestLifecycleRefusesEveryOwnerConflictBeforeMutation(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart", "remove"} {
		for _, object := range []string{"container", "workspace", "home", "ssh", "backup"} {
			for _, owner := range []string{"", "foreign"} {
				t.Run(verb+"/"+object+"/"+owner, func(t *testing.T) {
					responses := ownedLifecycleResponses(true)
					name := "sandboxed-agents.default.demo"
					if object == "container" {
						responses["container inspect "+name] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}},"State":{"Running":true}}]`}
					} else if object == "backup" {
						name = "sandboxed-agents-backup.default.demo"
						responses["container exists "+name] = lifecycleResponse{}
						responses["container inspect "+name] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}}]`}
					} else {
						name += "." + object
						responses["volume exists "+name] = lifecycleResponse{}
						responses["volume inspect "+name] = lifecycleResponse{Output: `[{"Labels":{"io.sandboxed-agents.owner":"` + owner + `"}}]`}
					}
					fakes := lifecycleHarness(t, responses)
					status, output := lifecycleRun(verb, "demo")
					if status == 0 || !strings.Contains(output, "owner conflict") || !strings.Contains(output, name) || !strings.Contains(output, "Podman") {
						t.Fatalf("status=%d output=%s", status, output)
					}
					calls := testutil.Invocations(t, fakes.PodmanLog)
					if len(lifecycleMutations(calls)) != 0 {
						t.Fatalf("mutations=%v", calls)
					}
					for _, call := range calls {
						if call[0] == "exec" {
							t.Fatalf("guard before ownership: %v", calls)
						}
					}
				})
			}
		}
	}
}

// TestLifecycleChecksUnknownAndInterruptedSandboxBeforeChanges proves absent names and owned backup containers refuse before mutation or session queries through RunWithHost.
func TestLifecycleChecksUnknownAndInterruptedSandboxBeforeChanges(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart", "remove"} {
		for _, kind := range []string{"unknown", "backup"} {
			t.Run(verb+"/"+kind, func(t *testing.T) {
				responses := ownedLifecycleResponses(true)
				want := "does not exist in this controller group"
				if kind == "unknown" {
					responses["container exists sandboxed-agents.default.demo"] = lifecycleResponse{Exit: 1}
				} else {
					want = "update demo"
					responses["container exists sandboxed-agents-backup.default.demo"] = lifecycleResponse{}
					responses["container inspect sandboxed-agents-backup.default.demo"] = lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}
				}
				fakes := lifecycleHarness(t, responses)
				status, output := lifecycleRun(verb, "demo")
				if status == 0 || !strings.Contains(output, want) {
					t.Fatalf("status=%d output=%s want=%s", status, output, want)
				}
				calls := testutil.Invocations(t, fakes.PodmanLog)
				if len(lifecycleMutations(calls)) != 0 {
					t.Fatalf("mutations=%v", calls)
				}
				for _, call := range calls {
					if call[0] == "exec" {
						t.Fatalf("guard before sandbox checks: %v", calls)
					}
				}
			})
		}
	}
}

// TestLifecyclePodmanFailuresDoNotReportSuccessOrRetry proves malformed inspections and failed mutations stop without success or automatic retries through RunWithHost.
func TestLifecyclePodmanFailuresDoNotReportSuccessOrRetry(t *testing.T) {
	for _, tc := range []struct {
		key      string
		response lifecycleResponse
		message  string
	}{
		{"container inspect sandboxed-agents.default.demo", lifecycleResponse{Output: "{"}, "invalid inspect answer"},
		{"container inspect sandboxed-agents.default.demo", lifecycleResponse{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}}}]`}, "running state is missing"},
		{"start sandboxed-agents.default.demo", lifecycleResponse{Exit: 23}, "exit status 23"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			responses := ownedLifecycleResponses(false)
			responses[tc.key] = tc.response
			fakes := lifecycleHarness(t, responses)
			status, output := lifecycleRun("start", "demo")
			if status == 0 || !strings.Contains(output, tc.message) || strings.Contains(output, "is running") {
				t.Fatalf("status=%d output=%s", status, output)
			}
			count := 0
			for _, call := range testutil.Invocations(t, fakes.PodmanLog) {
				if strings.Join(call, " ") == tc.key {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("failed call count=%d", count)
			}
		})
	}
}

// TestLifecycleWindowsSelectsOneTargetWithoutPreflight proves machine queries alone select and bind lifecycle operations while stripping ambient targets through RunWithHost.
func TestLifecycleWindowsSelectsOneTargetWithoutPreflight(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart"} {
		t.Run(verb, func(t *testing.T) {
			responses := map[string]lifecycleResponse{
				"machine list --format json": {Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`},
				"machine inspect chosen":     {Output: `[{"Name":"chosen","State":"running","Rootful":false}]`},
			}
			for key, response := range ownedLifecycleResponses(true) {
				responses["--connection chosen "+key] = response
			}
			fakes := lifecycleHarness(t, responses)
			t.Setenv("CONTAINER_HOST", "unexpected")
			t.Setenv("CONTAINER_CONNECTION", "unexpected")
			t.Setenv("CONTAINER_SSHKEY", "unexpected")
			envLog := filepath.Join(t.TempDir(), "environment.json")
			t.Setenv("TEST_PODMAN_ENV_LOG", envLog)
			var output bytes.Buffer
			status := cli.RunWithHost([]string{verb, "demo"}, &output, &output, cli.Host{Platform: "windows"})
			if status != 0 {
				t.Fatalf("status=%d output=%s", status, output.String())
			}
			calls := testutil.Invocations(t, fakes.PodmanLog)
			wantPrefix := [][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}
			if len(calls) < 3 || !reflect.DeepEqual(calls[:2], wantPrefix) {
				t.Fatalf("machine calls=%v", calls)
			}
			for _, call := range calls[2:] {
				if len(call) < 3 || call[0] != "--connection" || call[1] != "chosen" || call[2] == "info" || call[2] == "--version" || call[2] == "version" {
					t.Fatalf("unbound/preflight call=%v", call)
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
				if strings.HasPrefix(entry, "CONTAINER_HOST=") || strings.HasPrefix(entry, "CONTAINER_CONNECTION=") || strings.HasPrefix(entry, "CONTAINER_SSHKEY=") {
					t.Fatalf("ambient target retained: %s", entry)
				}
			}
		})
	}
}

// TestLifecycleDeferredSSHSetupDoesNotClaimSuccess proves start --ssh-config refuses its unavailable setup before starting a sandbox through RunWithHost.
func TestLifecycleDeferredSSHSetupDoesNotClaimSuccess(t *testing.T) {
	fakes := lifecycleHarness(t, ownedLifecycleResponses(false))
	status, output := lifecycleRun("start", "demo", "--ssh-config")
	if status == 0 || !strings.Contains(output, "SSH setup is not implemented") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	if calls := testutil.Invocations(t, fakes.PodmanLog); len(lifecycleMutations(calls)) != 0 {
		t.Fatalf("mutations=%v", calls)
	}
}
