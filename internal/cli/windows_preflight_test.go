package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// windowsHost creates a passing Windows host fixture with a selected running rootless WSL2
// machine.
func windowsHost(t *testing.T) *fixture {
	t.Helper()
	f := linuxHost(t)
	f.host.Platform = "windows"
	f.host.WindowsBuild = func() (uint32, error) { return 22621, nil }
	f.host.WindowsWorkstation = func() (bool, error) { return true, nil }
	f.host.WindowsNativeArchitecture = func() (uint16, error) { return 0x8664, nil }
	f.responses = map[string]response{
		"--connection chosen " + portInventory:      {Output: "[]"},
		"--version":                                 {Output: "podman version 4.7.0"},
		"machine list --format json":                {Output: `[{"Name":"chosen","Default":true,"VMType":"wsl"}]`},
		"machine inspect chosen":                    {Output: machineInspectAnswer},
		"--connection chosen version --format json": {Output: `{"Client":{"Version":"4.7.0"},"Server":{"Version":"4.7.0"}}`},
		"--connection chosen info --format json":    {Output: `{"host":{"security":{"rootless":true},"cgroupVersion":"v2"}}`},
		toolRestoreCall:                             {},
		toolListCall:                                {Output: "[]\n"},
	}
	f.sshResponses = map[string]response{
		windowsUIDRead:         {Output: "1000\n"},
		windowsCgroupRead:      {Output: "0::/user.slice/user-1000.slice/session-1.scope\n"},
		windowsControllerRead:  {Output: "cpu memory pids"},
		windowsSubtreeRead:     {Output: "cpu memory pids"},
		windowsAutomountExists: {},
		windowsAutomountRead:   {Output: ""},
	}
	return f
}

// machineIdentity is the selected machine's inspected identity file; its spaces prove the path
// travels as one argument.
const machineIdentity = `C:\machine key with spaces\machine`

// machineInspectAnswer is the fake inspect answer of a running rootless machine with its SSH
// configuration, as Podman reports it.
const machineInspectAnswer = `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\machine key with spaces\\machine","Port":54321,"RemoteUsername":"core"}}]`

// machineQueryPrefix matches the host ssh invocation of one machine query up to its remote
// command: any client options, then the inspected identity, port, and user.
const machineQueryPrefix = "* -i " + machineIdentity + " -p 54321 core@localhost "

// windowsUIDRead and windowsCgroupRead name the delegation discovery queries inside the machine.
const windowsUIDRead = machineQueryPrefix + "id -u"
const windowsCgroupRead = machineQueryPrefix + "cat /proc/self/cgroup"

// windowsControllerRead names the read-only delegated controller query.
const windowsControllerRead = machineQueryPrefix + "cat /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"
const windowsSubtreeRead = machineQueryPrefix + "cat /sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control"

// machineQueryOptions are the client options every machine query carries: no user configuration,
// a private known_hosts file in the query directory that pins the machine's Ed25519 key on first
// contact and refuses a changed key, no agent, no terminal, and a quiet client.
var machineQueryOptions = []string{"-F", "none", "-o", "BatchMode=yes", "-o", "RequestTTY=no", "-o", "StrictHostKeyChecking=accept-new", "-o", "UpdateHostKeys=no", "-o", "UserKnownHostsFile=known_hosts", "-o", "GlobalKnownHostsFile=none", "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes", "-o", "CheckHostIP=no", "-o", "LogLevel=ERROR", "-o", "SetEnv=LC_ALL="}

// machineArgv returns the remote command of a recorded host ssh call that addresses the fixture's
// machine with the expected options, identity, port, and user, or nil for any other call.
func machineArgv(call []string) []string {
	destination := slices.Index(call, "core@localhost")
	if destination < 0 {
		return nil
	}
	expected := append(append([]string{}, machineQueryOptions...), "-i", machineIdentity, "-p", "54321", "core@localhost")
	if !reflect.DeepEqual(call[:destination+1], expected) {
		return nil
	}
	return call[destination+1:]
}

// machineQueryCalls returns the remote commands of every recorded machine query, in order.
func machineQueryCalls(t *testing.T, f *fixture) [][]string {
	t.Helper()
	var queries [][]string
	for _, call := range f.sshCalls(t) {
		if argv := machineArgv(call); argv != nil {
			queries = append(queries, argv)
		}
	}
	return queries
}

// noMachineSSHThroughPodman rejects any Podman call that runs a command inside the machine.
func noMachineSSHThroughPodman(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		if len(call) > 1 && call[0] == "machine" && call[1] == "ssh" {
			t.Fatalf("machine command through podman machine ssh: %v", call)
		}
	}
}

// TestWindowsDelegationRequiresEnabledSubtreeControllers proves machine controllers must also be enabled and failed subtree reads identify their path through RunWithHost and fake machine SSH processes.
func TestWindowsDelegationRequiresEnabledSubtreeControllers(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		value         response
		status        int
	}{
		{"pids-not-enabled", "delegated cgroup pids controller is required", response{Output: "cpu memory"}, 1},
		{"all-enabled", "host prerequisites satisfied", response{Output: "cpu memory pids"}, 0},
		{"subtree-unreadable", "read delegated cgroup controllers:", response{Stderr: "permission denied", Exit: 125}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := windowsHost(t)
			f.sshResponses[windowsSubtreeRead] = tc.value
			status, output, calls := f.run(t, "check")
			if status != tc.status || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%q; want status=%d message=%q", status, output, tc.status, tc.message)
			}
			if tc.value.Exit != 0 && (!strings.Contains(output, "cgroup.subtree_control") || !strings.Contains(output, "permission denied")) {
				t.Fatalf("subtree read failure omitted path/cause: %s", output)
			}
			noMutation(t, calls)
		})
	}
}

const windowsAutomountRead = machineQueryPrefix + "cat /etc/wsl.conf"
const windowsAutomountExists = machineQueryPrefix + "test -e /etc/wsl.conf"

// TestWindowsAbsentAutomountFileUsesDefaultWithoutRead proves an absent wsl.conf defaults to /mnt/ after an existence check without a read through RunWithHost and fake Podman processes.
func TestWindowsAbsentAutomountFileUsesDefaultWithoutRead(t *testing.T) {
	f := windowsHost(t)
	f.sshResponses[windowsAutomountExists] = response{Exit: 1}
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "machine chosen automount root /mnt/") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMachineSSHThroughPodman(t, calls)
	existenceQueries := 0
	for _, call := range f.sshCalls(t) {
		if reflect.DeepEqual(machineArgv(call), []string{"test", "-e", "/etc/wsl.conf"}) {
			existenceQueries++
		} else if argv := machineArgv(call); len(argv) > 0 && (argv[0] == "sh" || reflect.DeepEqual(argv, []string{"cat", "/etc/wsl.conf"})) {
			t.Fatalf("absent configuration was read: %v", call)
		}
	}
	if existenceQueries != 1 {
		t.Fatalf("existence queries=%d calls=%v", existenceQueries, calls)
	}
	noMutation(t, calls)
}

// TestWindowsAbsentAutomountFileIgnoresStderrWarning proves exit status 1 with empty stdout still defaults without reading despite stderr warnings through RunWithHost and fake Podman processes.
func TestWindowsAbsentAutomountFileIgnoresStderrWarning(t *testing.T) {
	f := windowsHost(t)
	f.sshResponses[windowsAutomountExists] = response{Stderr: "logrus warning\n", Exit: 1}
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "machine chosen automount root /mnt/") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMachineSSHThroughPodman(t, calls)
	existenceQueries := 0
	for _, call := range f.sshCalls(t) {
		if reflect.DeepEqual(machineArgv(call), []string{"test", "-e", "/etc/wsl.conf"}) {
			existenceQueries++
		}
		if reflect.DeepEqual(machineArgv(call), []string{"cat", "/etc/wsl.conf"}) {
			t.Fatalf("absent configuration was read: %v", calls)
		}
	}
	if existenceQueries != 1 {
		t.Fatalf("existence queries=%d calls=%v", existenceQueries, calls)
	}
	noMutation(t, calls)
}

// TestWindowsAutomountExistenceFailureStopsWork proves existence-query transport diagnostics are not mistaken for absence and stop mutations through RunWithHost and fake Podman.
func TestWindowsAutomountExistenceFailureStopsWork(t *testing.T) {
	for _, code := range []int{1, 125} {
		for _, args := range [][]string{{"check"}, {"build"}, {"up", "demo"}} {
			t.Run(strings.Join(args, "-")+"-"+strconv.Itoa(code), func(t *testing.T) {
				f := windowsHost(t)
				f.sshResponses[windowsAutomountExists] = response{Output: "transport unavailable", Exit: code}
				status, output, calls := f.run(t, args...)
				if status != 1 || !strings.Contains(output, "automount root") || !strings.Contains(output, "chosen") || !strings.Contains(output, "transport unavailable") {
					t.Fatalf("status=%d output=%q", status, output)
				}
				for _, call := range f.sshCalls(t) {
					if reflect.DeepEqual(machineArgv(call), []string{"cat", "/etc/wsl.conf"}) {
						t.Fatalf("configuration read after failed existence query: %v", call)
					}
				}
				noMutation(t, calls)
			})
		}
	}
}

// TestWindowsPreflightUsesNativeArchitecture proves native PE AMD64 is accepted independently of process architecture while emulated ARM64, other architectures, and query errors refuse work through RunWithHost and fake Podman processes.
func TestWindowsPreflightUsesNativeArchitecture(t *testing.T) {
	for _, tc := range []struct {
		name, process, message string
		native                 uint16
		lookupErr              error
		status                 int
	}{
		{"native-amd64", "386", "host prerequisites satisfied", 0x8664, nil, 0},
		{"emulated-arm64", "amd64", "Windows 11 x64 is required", 0xAA64, nil, 1},
		{"native-i386", "amd64", "Windows 11 x64 is required", 0x014c, nil, 1},
		{"lookup-failure", "amd64", "native architecture query: lookup unavailable", 0, errors.New("lookup unavailable"), 1},
		{"query-failure", "amd64", "native architecture query: query unavailable", 0, errors.New("query unavailable"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := windowsHost(t)
			f.host.Architecture = tc.process
			f.host.WindowsNativeArchitecture = func() (uint16, error) { return tc.native, tc.lookupErr }
			status, output, calls := f.run(t, "check")
			if status != tc.status || !strings.Contains(output, tc.message) {
				t.Fatalf("status=%d output=%q; want status=%d message=%q", status, output, tc.status, tc.message)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsAutomountConfigFailureStopsWork proves empty roots and unreadable configuration refuse work with a machine-specific diagnostic through RunWithHost and fake Podman processes.
func TestWindowsAutomountConfigFailureStopsWork(t *testing.T) {
	for _, config := range []string{"[automount]\nroot = \n", "[automount\nroot=/custom/\n", "\x00"} {
		t.Run(config, func(t *testing.T) {
			f := windowsHost(t)
			f.sshResponses[windowsAutomountRead] = response{Output: config}
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "automount root") || !strings.Contains(output, "chosen") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

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
			f.sshResponses[windowsControllerRead] = response{Output: "memory pids"}
		}},
		{"machine-memory", "delegated cgroup memory controller", func(t *testing.T, f *fixture) {
			f.sshResponses[windowsControllerRead] = response{Output: "cpu pids"}
		}},
		{"machine-pids", "delegated cgroup pids controller", func(t *testing.T, f *fixture) {
			f.sshResponses[windowsControllerRead] = response{Output: "cpu memory"}
		}},
		{"podman", "podman is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "podman")) }},
		{"ssh", "ssh is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh")) }},
		{"ssh-keygen", "ssh-keygen is required on PATH", func(t *testing.T, f *fixture) { os.Remove(fakePath(f, "ssh-keygen")) }},
		{"architecture", "Windows 11 x64 is required", func(t *testing.T, f *fixture) {
			f.host.WindowsNativeArchitecture = func() (uint16, error) { return 0xAA64, nil }
		}},
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
		ssh       bool
	}{
		{"list-failed", "machine list --format json", response{Exit: 125}, false},
		{"list-malformed", "machine list --format json", response{Output: "{"}, false},
		{"multiple-defaults", "machine list --format json", response{Output: `[{"Name":"one","Default":true},{"Name":"two","Default":true}]`}, false},
		{"inspect-failed", "machine inspect chosen", response{Exit: 125}, false},
		{"inspect-malformed", "machine inspect chosen", response{Output: "{"}, false},
		{"server-missing", "--connection chosen version --format json", response{Output: `{"Client":{"Version":"9.0.0"}}`}, false},
		{"server-failed", "--connection chosen version --format json", response{Exit: 125}, false},
		{"controllers-failed", windowsControllerRead, response{Exit: 125}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := windowsHost(t)
			if tc.ssh {
				f.sshResponses[tc.key] = tc.value
			} else {
				f.responses[tc.key] = tc.value
			}
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "step 2:") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsMachineQueryWithoutAnswerFailsWithinBound proves a machine query whose ssh client
// never answers fails the preflight within the query bound, naming the query and the exceeded
// deadline, instead of stalling the command, through RunWithHost and a fake ssh that waits for a
// rendezvous file that never appears.
func TestWindowsMachineQueryWithoutAnswerFailsWithinBound(t *testing.T) {
	defer cli.SetMachineQueryTimeout(300 * time.Millisecond)()
	f := windowsHost(t)
	f.sshResponses[windowsUIDRead] = response{Output: "1000\n", WaitFor: filepath.Join(t.TempDir(), "never")}
	start := time.Now()
	status, output, calls := f.run(t, "check")
	elapsed := time.Since(start)
	if status != 1 || elapsed > 5*time.Second {
		t.Fatalf("status=%d after %v output=%q", status, elapsed, output)
	}
	if !strings.Contains(output, "machine chosen: ssh id -u: context deadline exceeded") {
		t.Fatalf("output lacks the bounded query failure: %q", output)
	}
	for _, requirement := range requiredControllerRequirements() {
		if !strings.Contains(output, requirement) {
			t.Fatalf("output lacks %q after the unanswered query: %q", requirement, output)
		}
	}
	noMutation(t, calls)
}

// TestWindowsAutomountExistenceTimeoutIsNotAbsence proves an existence query whose client the
// query bound stops, which also ends with exit status 1 and no output, fails the automount read
// with the exceeded deadline instead of defaulting to /mnt/, through RunWithHost and a fake ssh
// that waits for a rendezvous file that never appears.
func TestWindowsAutomountExistenceTimeoutIsNotAbsence(t *testing.T) {
	defer cli.SetMachineQueryTimeout(300 * time.Millisecond)()
	f := windowsHost(t)
	f.sshResponses[windowsAutomountExists] = response{Exit: 1, WaitFor: filepath.Join(t.TempDir(), "never")}
	status, output, calls := f.run(t, "check")
	if status != 1 || strings.Contains(output, "automount root /mnt/") {
		t.Fatalf("status=%d output=%q; a stopped existence query must not default the root", status, output)
	}
	if !strings.Contains(output, `read automount root for machine "chosen"`) || !strings.Contains(output, "test -e /etc/wsl.conf: context deadline exceeded") {
		t.Fatalf("output lacks the bounded existence query failure: %q", output)
	}
	for _, call := range f.sshCalls(t) {
		if reflect.DeepEqual(machineArgv(call), []string{"cat", "/etc/wsl.conf"}) {
			t.Fatalf("configuration read after a stopped existence query: %v", call)
		}
	}
	noMutation(t, calls)
}

// requiredControllerRequirements lists the delegated controller prerequisites an unanswered
// machine query leaves unverified.
func requiredControllerRequirements() []string {
	var requirements []string
	for _, name := range []string{"cpu", "memory", "pids"} {
		requirements = append(requirements, "delegated cgroup "+name+" controller is required")
	}
	return requirements
}

// TestWindowsControllerQueriesUseSimpleMachineCommands proves delegation discovery uses simple read-only argv inside the machine, each one host ssh call, through RunWithHost, fake Podman, and fake ssh.
func TestWindowsControllerQueriesUseSimpleMachineCommands(t *testing.T) {
	f := windowsHost(t)
	status, output, calls := f.run(t, "check")
	if status != 0 {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMachineSSHThroughPodman(t, calls)
	var delegationQueries [][]string
	automountExistenceQueries, automountReadQueries := 0, 0
	for _, argv := range machineQueryCalls(t, f) {
		if reflect.DeepEqual(argv, []string{"test", "-e", "/etc/wsl.conf"}) {
			automountExistenceQueries++
			continue
		}
		if reflect.DeepEqual(argv, []string{"cat", "/etc/wsl.conf"}) {
			automountReadQueries++
			continue
		}
		delegationQueries = append(delegationQueries, argv)
		if len(argv) != 2 || (argv[0] != "id" && argv[0] != "cat") {
			t.Fatalf("unexpected delegation argv: %v", argv)
		}
	}
	expectedDelegation := [][]string{{"id", "-u"}, {"cat", "/proc/self/cgroup"}, {"cat", "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"}, {"cat", "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control"}}
	if !reflect.DeepEqual(delegationQueries, expectedDelegation) {
		t.Fatalf("delegation queries=%v; want %v", delegationQueries, expectedDelegation)
	}
	if automountExistenceQueries != 1 || automountReadQueries != 1 {
		t.Fatalf("automount existence queries=%d read queries=%d", automountExistenceQueries, automountReadQueries)
	}
	noMutation(t, calls)
}

// TestWindowsMachineQueriesRunTheHostSSHClientDirectly proves check answers its six machine
// queries through the host's ssh client started by the executable itself, with the identity,
// port, and user that podman machine inspect reported and one private known_hosts directory for
// the whole preflight that is gone afterwards, and never through podman machine ssh, through
// RunWithHost, fake Podman, and fake ssh.
func TestWindowsMachineQueriesRunTheHostSSHClientDirectly(t *testing.T) {
	f := windowsHost(t)
	directories := filepath.Join(t.TempDir(), "ssh-directories.jsonl")
	t.Setenv("TEST_SSH_CWD_LOG", directories)
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "host prerequisites satisfied") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMachineSSHThroughPodman(t, calls)
	sshCalls := f.sshCalls(t)
	expected := [][]string{{"id", "-u"}, {"cat", "/proc/self/cgroup"}, {"cat", "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers"}, {"cat", "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.subtree_control"}, {"test", "-e", "/etc/wsl.conf"}, {"cat", "/etc/wsl.conf"}}
	if len(sshCalls) != len(expected) {
		t.Fatalf("ssh calls=%v; want %d machine queries", sshCalls, len(expected))
	}
	for i, call := range sshCalls {
		if argv := machineArgv(call); !reflect.DeepEqual(argv, expected[i]) {
			t.Fatalf("ssh call %d=%v; want machine query %v with the inspected identity, port, and user", i, call, expected[i])
		}
	}
	data, err := os.ReadFile(directories)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(expected) {
		t.Fatalf("ssh working directories=%v; want one per query", lines)
	}
	for _, line := range lines {
		if line != lines[0] {
			t.Fatalf("machine queries used different known_hosts directories: %v", lines)
		}
	}
	if _, err := os.Stat(lines[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private known_hosts directory %q remains after the preflight: %v", lines[0], err)
	}
	noMutation(t, calls)
}

// TestWindowsMachineQueryOutsideTheScriptFailsTheFixture proves the fake ssh refuses a machine
// query whose argv the fixture did not script, here one with a port the inspect answer changed,
// with exit status 125 that the preflight reports, so a changed client command line cannot pass
// unnoticed; the readiness probe's environment-driven answer beside such scripts is covered by
// the Windows update tests. Exercised through RunWithHost, fake Podman, and fake ssh.
func TestWindowsMachineQueryOutsideTheScriptFailsTheFixture(t *testing.T) {
	f := windowsHost(t)
	f.responses["machine inspect chosen"] = response{Output: strings.Replace(machineInspectAnswer, `"Port":54321`, `"Port":54322`, 1)}
	status, output, calls := f.run(t, "check")
	if status != 1 || !strings.Contains(output, "unscripted fake call") || !strings.Contains(output, "machine chosen: ssh id -u: exit status 125") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	if queries := machineQueryCalls(t, f); len(queries) != 0 {
		t.Fatalf("calls with the scripted port were recorded: %v", queries)
	}
	noMutation(t, calls)
}

// TestWindowsInvalidMachineSSHConfigurationFailsWithoutSSH proves a selected machine whose
// inspected SSH configuration is missing or unsafe fails the preflight naming that
// configuration, leaves the controller and automount facts unverified, and starts no ssh,
// through RunWithHost, fake Podman, and fake ssh.
func TestWindowsInvalidMachineSSHConfigurationFailsWithoutSSH(t *testing.T) {
	for _, tc := range []struct{ name, inspect string }{
		{"absent", `[{"Name":"chosen","State":"running","Rootful":false}]`},
		{"port-zero", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\keys\\machine","Port":0,"RemoteUsername":"core"}}]`},
		{"port-range", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\keys\\machine","Port":70000,"RemoteUsername":"core"}}]`},
		{"user-destination", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\keys\\machine","Port":54321,"RemoteUsername":"core@evil"}}]`},
		{"user-empty", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\keys\\machine","Port":54321,"RemoteUsername":""}}]`},
		{"identity-empty", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"","Port":54321,"RemoteUsername":"core"}}]`},
		{"identity-option", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"-oProxyCommand=evil","Port":54321,"RemoteUsername":"core"}}]`},
		{"identity-relative", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"keys\\machine","Port":54321,"RemoteUsername":"core"}}]`},
		{"identity-line-break", `[{"Name":"chosen","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":"C:\\keys\\ma\nchine","Port":54321,"RemoteUsername":"core"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := windowsHost(t)
			f.responses["machine inspect chosen"] = response{Output: tc.inspect}
			status, output, calls := f.run(t, "check")
			if status != 1 || !strings.Contains(output, `machine "chosen" SSH configuration`) {
				t.Fatalf("status=%d output=%q", status, output)
			}
			for _, requirement := range append(requiredControllerRequirements(), `read automount root for machine "chosen"`) {
				if !strings.Contains(output, requirement) || !strings.Contains(output, "cannot verify") {
					t.Fatalf("output lacks %q as unverified: %q", requirement, output)
				}
			}
			if sshCalls := f.sshCalls(t); len(sshCalls) != 0 {
				t.Fatalf("ssh started with an unsafe machine configuration: %v", sshCalls)
			}
			noMachineSSHThroughPodman(t, calls)
			noMutation(t, calls)
		})
	}
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

// TestWindowsCheckReportsCustomAutomountRoot proves a selected machine's configured root is recorded and reported without refusal through RunWithHost and fake Podman.
func TestWindowsCheckReportsCustomAutomountRoot(t *testing.T) {
	f := windowsHost(t)
	f.sshResponses[windowsAutomountRead] = response{Output: "[automount]\nroot = /custom/drives/\n"}
	status, output, calls := f.run(t, "check")
	if status != 0 || !strings.Contains(output, "machine chosen automount root /custom/drives/") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	noMutation(t, calls)
}

// TestWindowsCheckReportsDefaultAutomountRoot proves an existing empty configuration or absent automount root key records /mnt/ after existence and read queries through RunWithHost and fake Podman.
func TestWindowsCheckReportsDefaultAutomountRoot(t *testing.T) {
	for _, config := range []string{"", "[automount]\nenabled = true\n", "[network]\nroot=/unrelated/\n"} {
		t.Run(config, func(t *testing.T) {
			f := windowsHost(t)
			f.sshResponses[windowsAutomountRead] = response{Output: config}
			status, output, calls := f.run(t, "check")
			if status != 0 || !strings.Contains(output, "machine chosen automount root /mnt/") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			var rootQueries [][]string
			for _, call := range f.sshCalls(t) {
				if argv := machineArgv(call); reflect.DeepEqual(argv, []string{"test", "-e", "/etc/wsl.conf"}) || reflect.DeepEqual(argv, []string{"cat", "/etc/wsl.conf"}) {
					rootQueries = append(rootQueries, argv)
				}
			}
			expected := [][]string{{"test", "-e", "/etc/wsl.conf"}, {"cat", "/etc/wsl.conf"}}
			if !reflect.DeepEqual(rootQueries, expected) {
				t.Fatalf("automount queries=%v; want %v", rootQueries, expected)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsExistingAutomountFileReadFailureStopsWork proves an existing but unreadable wsl.conf reports its raw cause and selected machine without mutations through RunWithHost and fake Podman.
func TestWindowsExistingAutomountFileReadFailureStopsWork(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"build"}, {"up", "demo"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			f := windowsHost(t)
			f.sshResponses[windowsAutomountRead] = response{Output: "cat: /etc/wsl.conf: Permission denied", Exit: 1}
			status, output, calls := f.run(t, args...)
			if status != 1 || !strings.Contains(output, "automount root") || !strings.Contains(output, "chosen") || !strings.Contains(output, "Permission denied") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestWindowsAutomountQueryFailureStopsWork proves automount transport failures name the selected machine and refuse image or sandbox mutations through RunWithHost.
func TestWindowsAutomountQueryFailureStopsWork(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"build"}, {"up", "demo"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			f := windowsHost(t)
			f.sshResponses[windowsAutomountRead] = response{Output: "transport unavailable", Exit: 125}
			status, output, calls := f.run(t, args...)
			if status != 1 || !strings.Contains(output, "automount root") || !strings.Contains(output, "chosen") || !strings.Contains(output, "transport unavailable") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}
