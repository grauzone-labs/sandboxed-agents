package cli_test

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func scriptNewSandbox(t *testing.T, f *fixture, group, name string) {
	prefix := "sandboxed-agents." + group + "." + name
	connection := ""
	if f.host.Platform == "windows" {
		connection = "--connection chosen "
	}
	f.responses[connection+"container exists "+prefix] = response{Exit: 1}
	f.responses[connection+"image exists "+baseTag(t)] = response{}
	f.responses[connection+"volume exists *"] = response{Exit: 1}
	f.responses[connection+"volume create *"] = response{}
	f.responses[connection+"create *"] = response{Output: "container-id\n"}
	f.responses[connection+"start "+prefix] = response{Output: prefix + "\n"}
	f.responses[connection+"container inspect "+prefix] = response{Output: `[{"State":{"Running":true}}]`}
}

// TestUpCreatesOwnedSandbox proves up starts the named sandbox with three owned volumes and literal isolation defaults through RunWithHost and fake Podman argv.
func TestUpCreatesOwnedSandbox(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "agent01")
	status, output, calls := runUpExecutable(t, f, "up", "agent01")
	if status != 0 || !strings.Contains(output, "sandbox agent01 running") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "agent01", baseTag(t), "", "2222", "8589934592", "4", "2048", "1073741824")
}

func assertNewSandbox(t *testing.T, calls [][]string, connection, group, name, tag, workspace, port, memory, cpus, pids, shm string) {
	t.Helper()
	prefix := "sandboxed-agents." + group + "." + name
	if err := mountBoundaryError(calls, prefix, workspace); err != nil {
		t.Fatal(err)
	}
	target := []string{}
	if connection != "" {
		target = []string{"--connection", connection}
	}
	volumeNames := []string{prefix + ".workspace", prefix + ".home", prefix + ".ssh"}
	if workspace != "" {
		volumeNames = volumeNames[1:]
	}
	expected := [][]string{}
	for _, volume := range volumeNames {
		expected = append(expected, append(append([]string{}, target...), "volume", "exists", volume), append(append([]string{}, target...), "volume", "create", "--label", "io.sandboxed-agents.owner="+group, "--label", "io.sandboxed-agents.name="+name, volume))
	}
	workspaceMount := "type=volume,source=" + prefix + ".workspace,destination=/workspace"
	if workspace != "" {
		workspaceMount = "type=bind,source=" + workspace + ",destination=/workspace"
		if len(calls) >= 3 {
			for index, arg := range calls[len(calls)-3] {
				if arg == "--mount" {
					workspaceMount = calls[len(calls)-3][index+1]
					break
				}
			}
		}
	}
	create := []string{"create", "--name", prefix, "--label", "io.sandboxed-agents.owner=" + group, "--label", "io.sandboxed-agents.name=" + name, "--label", "io.sandboxed-agents.port=" + port, "--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw", "--memory", memory, "--cpus", cpus, "--pids-limit", pids, "--shm-size", shm, "--publish", "127.0.0.1:" + port + ":22/tcp", "--mount", workspaceMount, "--mount", "type=volume,source=" + prefix + ".home,destination=/home/agent", "--mount", "type=volume,source=" + prefix + ".ssh,destination=/etc/ssh", tag}
	expected = append(expected, append(append([]string{}, target...), create...), append(append([]string{}, target...), "start", prefix), append(append([]string{}, target...), "container", "inspect", prefix))
	// Compare every mutation argument; only the query prefix and optional build may precede these calls.
	if len(calls) < len(expected) || !reflect.DeepEqual(calls[len(calls)-len(expected):], expected) {
		t.Fatalf("create sequence=%v; want suffix=%v", calls, expected)
	}
}

func runUpExecutable(t *testing.T, f *fixture, args ...string) (int, string, [][]string) {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	files, err := json.Marshal(f.files)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(program, "-test.run=^TestUpExecutableBoundary$")
	command.Env = testutil.Env(os.Environ(), map[string]string{"TEST_UP_EXECUTABLE": "1", "TEST_UP_ARGS": string(arguments), "TEST_UP_FILES": string(files), "TEST_UP_PLATFORM": f.host.Platform, "TEST_PODMAN_RESPONSES": string(responses)})
	output, err := command.CombinedOutput()
	status := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return status, string(output), testutil.Invocations(t, f.fakes.PodmanLog)
}

// TestUpExecutableBoundary proves up's create and bind outcomes reach the subprocess exit status and output through RunWithHost against fake Podman and SSH.
func TestUpExecutableBoundary(t *testing.T) {
	if os.Getenv("TEST_UP_EXECUTABLE") != "1" {
		t.Skip("subprocess driver only")
	}
	var args []string
	var files map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("TEST_UP_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(os.Getenv("TEST_UP_FILES")), &files); err != nil {
		t.Fatal(err)
	}
	host := cli.Host{Platform: os.Getenv("TEST_UP_PLATFORM"), Architecture: "amd64", PortAvailable: func(uint16) error { return nil }, CurrentUser: func() (*user.User, error) { return &user.User{Username: "tester", Uid: "1000"}, nil }, ReadFile: func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(value), nil
	}, WindowsBuild: func() (uint32, error) { return 22000, nil }, WindowsWorkstation: func() (bool, error) { return true, nil }, WindowsNativeArchitecture: func() (uint16, error) { return 0x8664, nil }}
	os.Exit(cli.RunWithHost(args, os.Stdout, os.Stderr, host))
}

// TestUpBindsOnlyResolvedWorkspace proves a relative symlink workspace binds its resolved directory instead of a volume with no other host mounts through RunWithHost and fake Podman argv.
func TestUpBindsOnlyResolvedWorkspace(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "agent01")
	directory := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, alias)
	if err != nil {
		t.Fatal(err)
	}
	status, output, calls := runUpExecutable(t, f, "up", "agent01", relative)
	if status != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "agent01", baseTag(t), directory, "2222", "8589934592", "4", "2048", "1073741824")
}

// TestUpMountBoundaryIgnoresAmbientSockets proves create and build mount only the requested workspace and owned volumes despite host socket/display variables through RunWithHost and all fake Podman argv.
func TestUpMountBoundaryIgnoresAmbientSockets(t *testing.T) {
	for _, bind := range []bool{false, true} {
		t.Run(map[bool]string{false: "volumes", true: "workspace"}[bind], func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "agent01")
			f.responses["image exists "+baseTag(t)] = response{Exit: 1}
			f.responses["build *"] = response{}
			for key, value := range map[string]string{"SSH_AUTH_SOCK": "/run/host/ssh-agent.sock", "DOCKER_HOST": "unix:///var/run/docker.sock", "CONTAINER_HOST": "unix:///run/podman/podman.sock", "DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-0", "XDG_RUNTIME_DIR": "/run/host"} {
				t.Setenv(key, value)
			}
			args := []string{"up", "agent01"}
			workspace := ""
			if bind {
				workspace = t.TempDir()
				args = append(args, workspace)
			}
			status, output, calls := f.run(t, args...)
			if status != 0 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			assertNewSandbox(t, calls, "", "default", "agent01", baseTag(t), workspace, "2222", "8589934592", "4", "2048", "1073741824")
			if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected host SSH: %v", calls)
			}
		})
	}
}

// TestSocketWorkspaceFailsBeforePreflight proves a Unix socket cannot be the workspace bind and triggers no Podman or SSH process through RunWithHost.
func TestSocketWorkspaceFailsBeforePreflight(t *testing.T) {
	f := linuxHost(t)
	directory := t.TempDir()
	if runtime.GOOS == "linux" {
		// Unix socket addresses have a short kernel limit. The task's build
		// temporary root can exceed it, so use a short test-owned socket path.
		var err error
		directory, err = os.MkdirTemp("/tmp", "sa-socket-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(directory) })
	}
	socket := filepath.Join(directory, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	status, output, calls := f.run(t, "up", "agent01", socket)
	if status != 2 || !strings.Contains(output, "not a directory") || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("socket workspace reached SSH: %v", calls)
	}
}

// TestUpWorkspaceMountPreservesCSVPath proves commas and quotes in a workspace stay within exactly one source field without injecting options through RunWithHost and all fake Podman argv.
func TestUpWorkspaceMountPreservesCSVPath(t *testing.T) {
	for _, name := range []string{"project,destination=escape", `project"quoted"`} {
		t.Run(name, func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "agent01")
			workspace := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Skipf("path unavailable on host: %v", err)
			}
			status, output, calls := f.run(t, "up", "agent01", workspace)
			if status != 0 {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			assertNewSandbox(t, calls, "", "default", "agent01", baseTag(t), workspace, "2222", "8589934592", "4", "2048", "1073741824")
		})
	}
}

// TestUpResourceOverridesKeepIsolation proves custom limits and controller groups reach create without changing identities, security, or mounts through RunWithHost and all fake Podman argv.
func TestUpResourceOverridesKeepIsolation(t *testing.T) {
	f := linuxHost(t)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "team-2")
	scriptNewSandbox(t, f, "team-2", "agent01")
	status, output, calls := f.run(t, "up", "agent01", "--memory", "16g", "--cpus", "1.5", "--pids-limit", "4096", "--shm-size", "2g", "--port", "2300")
	if status != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "team-2", "agent01", baseTag(t), "", "2300", "17179869184", "1.5", "4096", "2147483648")
}

// TestUpCreationFailuresDoNotReportRunning proves failed volume/create/publication/start calls propagate failure and never start after a failed create through RunWithHost and fake Podman.
func TestUpCreationFailuresDoNotReportRunning(t *testing.T) {
	prefix := "sandboxed-agents.default.agent01"
	for _, tc := range []struct {
		key, message string
		existing     bool
	}{
		{"volume exists " + prefix + ".workspace", "volume adoption is not implemented", true},
		{"volume create *", "volume creation failed", false},
		{"create *", "SSH publication failed", false},
		{"start " + prefix, "start failed", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "agent01")
			failure := response{Stderr: tc.message, Exit: 125}
			if tc.existing {
				failure = response{}
			}
			f.responses[tc.key] = failure
			status, output, calls := f.run(t, "up", "agent01")
			if status != 1 || !strings.Contains(output, tc.message) || strings.Contains(output, "sandbox agent01 running") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			if err := mountBoundaryError(calls, prefix, ""); err != nil {
				t.Fatal(err)
			}
			if tc.key != "start "+prefix {
				for _, call := range calls {
					if len(call) > 0 && call[0] == "start" {
						t.Fatalf("started despite failed creation: %v", calls)
					}
				}
			}
			if tc.existing {
				noMutation(t, calls[:len(calls)-1])
				if strings.Join(calls[len(calls)-1], " ") != tc.key {
					t.Fatalf("changed existing volume: %v", calls)
				}
			}
			if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unexpected SSH calls: %v", calls)
			}
		})
	}
}

// TestWindowsUnboundUpAcceptsCustomAutomount proves a volume-only sandbox keeps the preflight-selected connection even with a custom WSL automount root through RunWithHost and all fake Podman argv.
func TestWindowsUnboundUpAcceptsCustomAutomount(t *testing.T) {
	f := windowsHost(t)
	f.responses[windowsAutomountRead] = response{Output: "[automount]\nroot=/custom/drives/\n"}
	scriptNewSandbox(t, f, "default", "agent01")
	status, output, calls := f.run(t, "up", "agent01")
	if status != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "chosen", "default", "agent01", baseTag(t), "", "2222", "8589934592", "4", "2048", "1073741824")
}

// TestUpInspectsCreatedSandboxRunning proves up checks the newly started sandbox's running state before claiming success through RunWithHost and fake Podman inspection.
func TestUpInspectsCreatedSandboxRunning(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "agent01")
			name := "sandboxed-agents.default.agent01"
			state := "false"
			if running {
				state = "true"
			}
			f.responses["container inspect "+name] = response{Output: `[{"State":{"Running":` + state + `}}]`}
			status, output, calls := f.run(t, "up", "agent01")
			if running {
				if status != 0 || !strings.Contains(output, "sandbox agent01 running") {
					t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
				}
			} else if status != 1 || !strings.Contains(output, name) || !strings.Contains(output, "not running") || strings.Contains(output, "sandbox agent01 running") {
				t.Fatalf("status=%d output=%q calls=%v; want stopped sandbox refusal", status, output, calls)
			}
			if len(calls) < 2 || strings.Join(calls[len(calls)-1], " ") != "container inspect "+name || strings.Join(calls[len(calls)-2], " ") != "start "+name {
				t.Fatalf("missing running inspection immediately after start: %v", calls)
			}
		})
	}
}

// TestUpVerifiesRunningStateAfterStart proves up reports success only for inspected State.Running and otherwise names the stopped sandbox through RunWithHost and fake Podman on Linux and Windows.
func TestUpVerifiesRunningStateAfterStart(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, running := range []bool{false, true} {
			t.Run(platform+"/"+map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
				f := linuxHost(t)
				connection := ""
				if platform == "windows" {
					f = windowsHost(t)
					connection = "--connection chosen "
				}
				scriptNewSandbox(t, f, "default", "agent01")
				name := "sandboxed-agents.default.agent01"
				state := "false"
				if running {
					state = "true"
				}
				f.responses[connection+"container inspect "+name] = response{Output: `[{"State":{"Running":` + state + `}}]`}
				status, output, calls := f.run(t, "up", "agent01")
				if running {
					if status != 0 || !strings.Contains(output, "sandbox agent01 running") {
						t.Fatalf("status=%d output=%q calls=%v; want inspected running success", status, output, calls)
					}
				} else if status != 1 || !strings.Contains(output, name) || !strings.Contains(output, "not running") || strings.Contains(output, "sandbox agent01 running") {
					t.Fatalf("status=%d output=%q calls=%v; want stopped sandbox error", status, output, calls)
				}
				wantLast := connection + "container inspect " + name
				if len(calls) == 0 || strings.Join(calls[len(calls)-1], " ") != wantLast {
					t.Fatalf("missing running-state inspection after start: %v", calls)
				}
				if len(calls) < 2 || strings.Join(calls[len(calls)-2], " ") != connection+"start "+name {
					t.Fatalf("running inspection did not follow start: %v", calls)
				}
				if err := mountBoundaryError(calls, name, ""); err != nil {
					t.Fatal(err)
				}
				if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
					t.Fatalf("unexpected host SSH calls: %v", calls)
				}
			})
		}
	}
}
