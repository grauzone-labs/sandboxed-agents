package cli_test

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestWindowsWorkspaceInstallGuardPrecedesPreflight proves Windows default install paths and aliases fail usage before Podman or SSH through RunWithHost's platform facts.
func TestWindowsWorkspaceInstallGuardPrecedesPreflight(t *testing.T) {
	for _, shape := range []string{"directory", "ancestor", "descendant", "alias"} {
		t.Run(shape, func(t *testing.T) {
			f := windowsHost(t)
			local := t.TempDir()
			t.Setenv("LOCALAPPDATA", local)
			install := filepath.Join(local, "Programs", "sandboxed-agents")
			descendant := filepath.Join(install, "project")
			if err := os.MkdirAll(descendant, 0700); err != nil {
				t.Fatal(err)
			}
			workspace := install
			switch shape {
			case "ancestor":
				workspace = filepath.Dir(install)
			case "descendant":
				workspace = descendant
			case "alias":
				workspace = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(install, workspace); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			status, output, calls := f.run(t, "up", "agent01", workspace)
			if status != 2 || !strings.Contains(output, "protected host path") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			if len(calls) != 0 || len(testutil.Invocations(t, f.fakes.SSHLog)) != 0 {
				t.Fatalf("process reached before protected path refusal: %v", calls)
			}
		})
	}
}

// TestWindowsWorkspaceRefusesCustomAutomountAfterPreflight proves bound up completes read-only Windows preflight then refuses custom roots before image builds or creation through RunWithHost and fake Podman.
func TestWindowsWorkspaceRefusesCustomAutomountAfterPreflight(t *testing.T) {
	f := windowsHost(t)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	scriptNewSandbox(t, f, "default", "agent01")
	f.responses["--connection chosen image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["--connection chosen build *"] = response{}
	f.sshResponses[windowsAutomountRead] = response{Output: "[automount]\nroot=/custom/drives/\n"}
	directory := t.TempDir()
	f.host.WorkspacePath = func(path string) (string, error) {
		if path != directory {
			t.Fatalf("workspace path=%q; want=%q", path, directory)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("existing workspace fixture: info=%v error=%v", info, err)
		}
		return `C:\workspace`, nil
	}
	status, output, calls := f.run(t, "up", "agent01", directory)
	if status != 1 || !strings.HasPrefix(output, "step 2:") || !strings.Contains(output, "custom WSL automount root") || !strings.Contains(output, "/custom/drives/") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if len(calls) != 5 || !reflect.DeepEqual(calls[len(calls)-1], []string{"--connection", "chosen", "info", "--format", "json"}) {
		t.Fatalf("refusal must follow full preflight and precede sandbox/build work: %v", calls)
	}
	noMutation(t, calls)
	queries := machineQueryCalls(t, f)
	if sshCalls := f.sshCalls(t); len(sshCalls) != 6 || len(queries) != 6 || !reflect.DeepEqual(queries[5], []string{"cat", "/etc/wsl.conf"}) {
		t.Fatalf("host SSH calls must be the six machine queries ending with the automount read: %v", sshCalls)
	}
}

// TestWorkspaceRefusesPackageLaunchersBeforePreflight proves a workspace containing a package launcher or its alias fails before Podman or SSH through RunWithHost.
func TestWorkspaceRefusesPackageLaunchersBeforePreflight(t *testing.T) {
	for _, name := range []string{"sandboxed-agents", "sandboxed-agents.exe", "sandboxed-agents.cmd", "sandboxed-agents.ps1"} {
		for _, alias := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "direct", true: "alias"}[alias], func(t *testing.T) {
				f := linuxHost(t)
				directory := t.TempDir()
				if err := os.WriteFile(filepath.Join(directory, name), []byte("launcher"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+directory)
				workspace := directory
				if alias {
					workspace = filepath.Join(t.TempDir(), "alias")
					if err := os.Symlink(directory, workspace); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				}
				status, output, calls := f.run(t, "up", "agent01", workspace)
				if status != 2 || !strings.Contains(output, "protected host path") {
					t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
				}
				if len(calls) != 0 || len(testutil.Invocations(t, f.fakes.SSHLog)) != 0 {
					t.Fatalf("process reached before launcher refusal: %v", calls)
				}
			})
		}
	}
}

// TestWorkspaceRefusesBuildInputsBeforePreflight proves active build inputs, aliases, and directories containing future build inputs fail before processes while unrelated temporary projects remain bindable through RunWithHost.
func TestWorkspaceRefusesBuildInputsBeforePreflight(t *testing.T) {
	for _, shape := range []string{"directory", "descendant", "ancestor", "alias", "unrelated project"} {
		t.Run(shape, func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "agent01")
			root := t.TempDir()
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, root)
			}
			build := filepath.Join(root, "sandboxed-agents-build-fixture")
			descendant := filepath.Join(build, "base")
			sibling := filepath.Join(root, "project")
			for _, path := range []string{descendant, sibling} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			workspace := build
			switch shape {
			case "descendant":
				workspace = descendant
			case "ancestor":
				workspace = root
			case "alias":
				workspace = filepath.Join(root, "build-alias")
				if err := os.Symlink(build, workspace); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "unrelated project":
				workspace = sibling
			}
			status, output, calls := f.run(t, "up", "agent01", workspace)
			if shape == "unrelated project" {
				if status != 0 {
					t.Fatalf("unrelated temporary project refused: status=%d output=%q calls=%v", status, output, calls)
				}
				assertNewSandbox(t, calls, "", "default", "agent01", baseTag(t), sibling, "2222", "8589934592", "4", "2048", "1073741824")
				return
			}
			if status != 2 || !strings.Contains(output, "protected host path") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			if len(calls) != 0 || len(testutil.Invocations(t, f.fakes.SSHLog)) != 0 {
				t.Fatalf("process reached before build-input refusal: %v", calls)
			}
		})
	}
}

// TestWorkspaceProtectedHostPathsPrecedeBrokenPreflight proves SSH, host state, executable directories, and their aliases fail usage before any host query through RunWithHost with Linux and Windows facts.
func TestWorkspaceProtectedHostPathsPrecedeBrokenPreflight(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, kind := range []string{"SSH", "host state", "executable"} {
			for _, shape := range []string{"directory", "ancestor", "alias"} {
				t.Run(platform+"/"+kind+"/"+shape, func(t *testing.T) {
					f := linuxHost(t)
					if platform == "windows" {
						f = windowsHost(t)
					}
					f.host.CurrentUser = func() (*user.User, error) {
						t.Fatal("preflight read user before protected path guard")
						return nil, nil
					}
					f.responses["--version"] = response{Output: "broken preflight", Exit: 125}
					home, state := t.TempDir(), t.TempDir()
					t.Setenv("HOME", home)
					t.Setenv("USERPROFILE", home)
					t.Setenv("XDG_STATE_HOME", state)
					t.Setenv("LOCALAPPDATA", state)
					protected := filepath.Join(home, ".ssh")
					switch kind {
					case "host state":
						protected = filepath.Join(state, "sandboxed-agents")
					case "executable":
						executable, err := os.Executable()
						if err != nil {
							t.Fatal(err)
						}
						protected = filepath.Dir(executable)
					}
					if kind != "executable" {
						if err := os.MkdirAll(protected, 0700); err != nil {
							t.Fatal(err)
						}
					}
					workspace := protected
					if shape == "ancestor" {
						workspace = filepath.Dir(protected)
					}
					if shape == "alias" {
						workspace = filepath.Join(t.TempDir(), "alias")
						if err := os.Symlink(protected, workspace); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					}
					status, output, calls := f.run(t, "up", "agent01", workspace)
					if status != 2 || !strings.Contains(output, "protected host path") || strings.Contains(output, "broken preflight") {
						t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
					}
					if len(calls) != 0 || len(testutil.Invocations(t, f.fakes.SSHLog)) != 0 {
						t.Fatalf("process reached before protected path guard: %v", calls)
					}
				})
			}
		}
	}
}

// TestWindowsWorkspaceJunctionPrecedesPreflight proves an actual Windows junction alias of the default install directory fails before Podman through RunWithHost on native Windows.
func TestWindowsWorkspaceJunctionPrecedesPreflight(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows junction check requires Windows")
	}
	f := windowsHost(t)
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	install := filepath.Join(local, "Programs", "sandboxed-agents")
	if err := os.MkdirAll(install, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "install-junction")
	command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", alias, install)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create Windows junction: %v output=%s", err, output)
	}
	status, output, calls := f.run(t, "up", "agent01", alias)
	if status != 2 || !strings.Contains(output, "protected host path") || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("junction workspace reached host SSH: %v", calls)
	}
}

// TestWindowsNonDriveWorkspaceFailsBeforePreflight proves existing UNC and other non-drive workspace facts fail step 1 without Podman or SSH through RunWithHost.
func TestWindowsNonDriveWorkspaceFailsBeforePreflight(t *testing.T) {
	for _, canonical := range []string{`\\server\share\workspace`, `/existing/non-drive/workspace`} {
		t.Run(canonical, func(t *testing.T) {
			f := windowsHost(t)
			directory := t.TempDir()
			f.host.WorkspacePath = func(path string) (string, error) {
				if path != directory {
					t.Fatalf("workspace path=%q; want=%q", path, directory)
				}
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("existing workspace fixture: info=%v error=%v", info, err)
				}
				return canonical, nil
			}
			status, output, calls := f.run(t, "up", "agent01", directory)
			if status != 2 || !strings.Contains(output, "step 1:") || !strings.Contains(output, "absolute Windows drive path") || len(calls) != 0 {
				t.Fatalf("status=%d output=%q calls=%v; want step 1 drive refusal before preflight", status, output, calls)
			}
			if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("unsupported workspace reached host SSH: %v", calls)
			}
		})
	}
}

// TestWindowsWorkspaceMissingStateUsesHostStateDiagnostic proves a missing Windows state root reports the shared host-state requirement with the generic usage line and no help pointer before external queries through RunWithHost.
func TestWindowsWorkspaceMissingStateUsesHostStateDiagnostic(t *testing.T) {
	f := windowsHost(t)
	t.Setenv("LOCALAPPDATA", "")
	status, output, calls := f.run(t, "up", "agent01", t.TempDir())
	want := "step 1: LOCALAPPDATA is required for host state\nusage: sandboxed-agents COMMAND [ARG...]\n"
	if status != 2 || output != want || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v; want shared host-state diagnostic=%q", status, output, calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("missing state reached host SSH: %v", calls)
	}
}

// TestParsedCommandMissingStateKeepsGenericUsage proves a lifecycle command that parses but
// finds no Windows state root keeps the host-state message, exit status 2, and the generic
// usage line without a help pointer, before external queries through RunWithHost.
func TestParsedCommandMissingStateKeepsGenericUsage(t *testing.T) {
	f := windowsHost(t)
	t.Setenv("LOCALAPPDATA", "")
	status, output, calls := f.run(t, "stop", "agent01")
	want := "step 1: LOCALAPPDATA is required for host state\nusage: sandboxed-agents COMMAND [ARG...]\n"
	if status != 2 || output != want || len(calls) != 0 {
		t.Fatalf("status=%d output=%q calls=%v; want generic host-state diagnostic=%q", status, output, calls, want)
	}
}

// TestParsedCommandHomeLookupFailureKeepsGenericUsage proves a lifecycle command that parses but
// cannot find the user's home directory keeps exit status 2 and the generic usage line without a
// help pointer through RunWithHost.
func TestParsedCommandHomeLookupFailureKeepsGenericUsage(t *testing.T) {
	f := linuxHost(t)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	status, output, calls := f.run(t, "up", "agent01", t.TempDir())
	if status != 2 || !strings.HasSuffix(output, "\nusage: sandboxed-agents COMMAND [ARG...]\n") || strings.Contains(output, "--help") {
		t.Fatalf("status=%d output=%q calls=%v; want exit 2, the generic usage line, and no --help pointer", status, output, calls)
	}
}
