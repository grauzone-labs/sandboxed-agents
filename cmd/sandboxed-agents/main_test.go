package main_test

import (
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var executable string
var fakeExecutable string

// TestMain prepares the built CLI and fake process binaries for the suite, reuses them in nested runs, and removes its temporary build directory after testing.
func TestMain(m *testing.M) {
	// Nested test runs reuse parent-built binaries with an isolated PATH.
	executable = os.Getenv("TEST_SANDBOXED_AGENTS_EXECUTABLE")
	fakeExecutable = os.Getenv("TEST_FAKE_EXECUTABLE")
	if executable != "" && fakeExecutable != "" {
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
	fakeExecutable = filepath.Join(dir, "fake"+suffix)
	fakeBuild := exec.Command("go", "build", "-o", fakeExecutable, "../../internal/testutil/testdata/fake.go")
	if output, err := fakeBuild.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
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
	if os.Getenv("TEST_OFFLINE_GUARD_NESTED") == "1" {
		t.Skip("nested offline run of the guard; the outer guard run covers it")
	}
	fakes := testutil.Install(t, fakeExecutable)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(testExecutable, "-test.v", "-test.run=.")
	child.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{
		"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable,
		"TEST_OFFLINE_GUARD_NESTED": "1",
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
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable,
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
				"TEST_SANDBOXED_AGENTS_EXECUTABLE": executable, "TEST_FAKE_EXECUTABLE": fakeExecutable,
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

// TestCommandGrammar proves that command forms produce their expected success, not-implemented, or usage status through the built CLI binary without Podman or SSH calls.
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
			command := exec.Command(executable, tc.args...)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			status := exitStatus(t, err)
			if status != tc.status {
				t.Fatalf("status=%d want=%d output=%q", status, tc.status, output)
			}
			if (tc.status == 2) != strings.Contains(string(output), "usage:") {
				t.Fatalf("unexpected diagnostic: %s", output)
			}
		})
	}
	assertNoExternalCalls(t, fakes)
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
	for _, program := range []struct{ name, path string }{
		{"Podman", fakes.PodmanLog}, {"SSH", fakes.SSHLog},
	} {
		if calls := testutil.Invocations(t, program.path); len(calls) != 0 {
			t.Fatalf("offline %s calls: %v", program.name, calls)
		}
	}
}

// TestAgentAndToolchainSelectionsUseDeliveredCatalogs proves that unknown selections are rejected and "none" selects no toolchains through the built CLI binary without external calls.
func TestAgentAndToolchainSelectionsUseDeliveredCatalogs(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	cases := []struct {
		option, value string
		status        int
		diagnostic    string
	}{
		{"agents", "bogus", 2, `unknown agent "bogus": no valid names are available yet`},
		{"with", "bogus", 2, `unknown toolchain "bogus": no valid names are available yet`},
		{"with", "none", 1, "up: not implemented"},
	}
	for _, tc := range cases {
		t.Run(tc.option+"="+tc.value, func(t *testing.T) {
			command := exec.Command(executable, "up", "demo", "--"+tc.option, tc.value)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status || !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("status=%d output=%q; want status=%d diagnostic=%q", status, output, tc.status, tc.diagnostic)
			}
		})
	}
	assertNoExternalCalls(t, fakes)
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

// TestControllerGroupAndSandboxNames proves that valid names reach the stub and invalid controller group or sandbox names produce usage errors through the built CLI binary without external calls.
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
			command := exec.Command(executable, "up", tc.name)
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"SANDBOXED_AGENTS_GROUP": tc.group})
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
		})
	}
	assertNoExternalCalls(t, fakes)
}

// TestUnsetControllerGroupUsesDefault proves that an unset controller group lets up reach its not-implemented stub through the built CLI binary without Podman or SSH calls.
func TestUnsetControllerGroupUsesDefault(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	command := exec.Command(executable, "up", "demo")
	for _, entry := range fakes.Env(os.Environ()) {
		if !strings.HasPrefix(entry, "SANDBOXED_AGENTS_GROUP=") {
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.CombinedOutput()
	if status := exitStatus(t, err); status != 1 || string(output) != "up: not implemented\n" {
		t.Fatalf("unset controller group: status=%d output=%q; want status=1 and not implemented", status, output)
	}
	assertNoExternalCalls(t, fakes)
}

// TestVersionIgnoresInvalidControllerGroup proves that version succeeds with its expected output despite an invalid or empty controller group through the built CLI binary without external calls.
func TestVersionIgnoresInvalidControllerGroup(t *testing.T) {
	for _, group := range []string{"Bad", ""} {
		t.Run(group, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			command := exec.Command(executable, "version")
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"SANDBOXED_AGENTS_GROUP": group})
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != 0 || string(output) != "sandboxed-agents development\n" {
				t.Fatalf("version: status=%d output=%q; want status=0 and version", status, output)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// TestResourceOptionGrammar proves that resource options accept supported numeric forms and bounds and reject malformed or out-of-range values through the built CLI binary without external calls.
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
			command := exec.Command(executable, "up", "demo", "--"+tc.option, tc.value)
			command.Env = fakes.Env(os.Environ())
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d want=%d output=%s", status, tc.status, output)
			}
			if tc.option == "cpus" && tc.status == 1 && string(output) != "up: not implemented\n" {
				t.Fatalf("output=%q; want the not-implemented path", output)
			}
		})
	}
	assertNoExternalCalls(t, fakes)
}

// TestWorkspacePathsFailBeforeExternalCalls proves that a workspace directory reaches the stub while missing, non-directory, protected, and aliased protected paths fail through the built CLI binary before external calls.
func TestWorkspacePathsFailBeforeExternalCalls(t *testing.T) {
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
			command := exec.Command(executable, "up", "demo", tc.path, "--with", "none")
			command.Env = testutil.Env(fakes.Env(os.Environ()), map[string]string{"HOME": home, "USERPROFILE": home, "XDG_STATE_HOME": state, "LOCALAPPDATA": state})
			output, err := command.CombinedOutput()
			if status := exitStatus(t, err); status != tc.status {
				t.Fatalf("status=%d want=%d output=%s", status, tc.status, output)
			}
		})
	}
	assertNoExternalCalls(t, fakes)
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
