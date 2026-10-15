package manager

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestProductionEffectsWireRealIdentityReads proves that the production effects constructor wires the real group and full ID readers through its function-reference seam without invoking them.
func TestProductionEffectsWireRealIdentityReads(t *testing.T) {
	effects := newEffects(systemCalls{})
	for _, reader := range []struct {
		name      string
		got, want any
	}{
		{"groups", effects.Groups, os.Getgroups},
		{"uids", effects.UIDs, unix.Getresuid},
		{"gids", effects.GIDs, unix.Getresgid},
	} {
		if got, want := reflect.ValueOf(reader.got).Pointer(), reflect.ValueOf(reader.want).Pointer(); got != want {
			t.Errorf("%s reader pointer=%#x; want production reader %#x", reader.name, got, want)
		}
	}
}

func stubPassingAgentIDReads(effects *Effects) {
	effects.Groups = func() ([]int, error) { return nil, nil }
	effects.UIDs = func() (int, int, int) { return 1000, 1000, 1000 }
	effects.GIDs = func() (int, int, int) { return 1000, 1000, 1000 }
}

func assertAgentCredentials(t *testing.T, command *exec.Cmd, noSetGroups bool) {
	t.Helper()
	if command.SysProcAttr == nil || command.SysProcAttr.Credential == nil {
		t.Error("process has no explicit OS credential")
		return
	}
	credential := command.SysProcAttr.Credential
	if credential.Uid != 1000 || credential.Gid != 1000 || credential.NoSetGroups != noSetGroups || len(credential.Groups) != 0 {
		t.Errorf("credential=%+v; want 1000:1000, empty groups, NoSetGroups=%v", credential, noSetGroups)
	}
}

// TestProductionDirectSessionsUseAgentEnvironment proves that direct session queries replace caller home and user variables exactly once and remove caller tmux settings through the production exec.Cmd seam.
func TestProductionDirectSessionsUseAgentEnvironment(t *testing.T) {
	starts := 0
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable: func() (string, error) { t.Error("direct session query tried to re-exec"); return "", nil },
		Environment: func() []string {
			return []string{"HOME=/home/caller", "HOME=/caller/duplicate", "USER=caller-user", "LOGNAME=caller-logname", "TMUX=/caller/socket,2,1", "TMUX_TMPDIR=/caller/tmux", "KEEP=direct-marker"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			wanted := map[string]string{"HOME": "/home/agent", "USER": "agent", "LOGNAME": "agent", "KEEP": "direct-marker"}
			counts := make(map[string]int)
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				counts[key]++
				if key == "TMUX" || key == "TMUX_TMPDIR" {
					t.Errorf("direct session query inherited caller variable %q", entry)
				}
				if expected, ok := wanted[key]; ok && value != expected {
					t.Errorf("environment variable %q; want %s=%s", entry, key, expected)
				}
			}
			for key := range wanted {
				if counts[key] != 1 {
					t.Errorf("environment=%q count(%s)=%d; want exactly one", command.Env, key, counts[key])
				}
			}
			return nil
		}})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"sessions"}, io.Discard, io.Discard, effects); status != 0 || starts != 1 {
		t.Fatalf("status=%d starts=%d; want one direct tmux session query", status, starts)
	}
}

// TestProductionSessionWorkerHasAgentCredentials proves that the production adapter re-execs its trusted executable with 1000:1000 credentials, cleared groups, and agent home through the injected exec.Cmd start seam.
func TestProductionSessionWorkerHasAgentCredentials(t *testing.T) {
	var calls int
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 0, GID: 0} },
		Executable: func() (string, error) { return "/trusted/manager", nil },
		Environment: func() []string {
			return []string{"HOME=/root", "USER=root", "LOGNAME=root", "TMUX=/root/socket,1,0", "TMUX_TMPDIR=/root/tmux", "KEEP=value"}
		},
		Start: func(command *exec.Cmd) error {
			calls++
			if command.Path != "/trusted/manager" || !reflect.DeepEqual(command.Args, []string{"/trusted/manager", "__worker", "sessions"}) {
				t.Errorf("command=%+v", command)
			}
			assertAgentCredentials(t, command, false)
			for _, wanted := range []string{"HOME=/home/agent", "USER=agent", "LOGNAME=agent", "KEEP=value"} {
				found := false
				for _, entry := range command.Env {
					if entry == wanted {
						found = true
					}
				}
				if !found {
					t.Errorf("environment=%q missing %q", command.Env, wanted)
				}
			}
			for _, entry := range command.Env {
				if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_TMPDIR=") || entry == "HOME=/root" || entry == "USER=root" || entry == "LOGNAME=root" {
					t.Errorf("worker inherits root session environment %q", entry)
				}
			}
			return nil
		}})
	if status := RunWithEffects([]string{"sessions"}, io.Discard, io.Discard, effects); status != 0 || calls != 1 {
		t.Fatalf("status=%d starts=%d; want one successful trusted worker", status, calls)
	}
}

// TestProductionMissingProgramReturnsFailure proves that missing tmux propagates lookup failure through the production session query adapter without starting a process.
func TestProductionMissingProgramReturnsFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stderr bytes.Buffer
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable:  func() (string, error) { t.Error("worker session query tried to re-exec"); return "", nil },
		Environment: func() []string { return nil },
		Start: func(command *exec.Cmd) error {
			if !errors.Is(command.Err, exec.ErrNotFound) {
				t.Errorf("lookup error=%v; want missing program", command.Err)
			}
			return command.Err
		}})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"__worker", "sessions"}, io.Discard, &stderr, effects); status != 1 || !strings.Contains(stderr.String(), "tmux") {
		t.Fatalf("status=%d stderr=%q; want failed program lookup", status, stderr.String())
	}
}

// TestProductionExecutableFailureStartsNothing proves that trusted executable lookup failure propagates without a root process fallback through the production re-exec adapter seam.
func TestProductionExecutableFailureStartsNothing(t *testing.T) {
	var stderr bytes.Buffer
	effects := newEffects(systemCalls{Identity: func() Identity { return Identity{UID: 0, GID: 0} },
		Executable:  func() (string, error) { return "", errors.New("trusted executable unavailable") },
		Environment: func() []string { t.Error("environment read after executable failure"); return nil },
		Start:       func(*exec.Cmd) error { t.Error("process started after executable failure"); return nil }})
	if status := RunWithEffects([]string{"sessions"}, io.Discard, &stderr, effects); status != 1 || !strings.Contains(stderr.String(), "trusted executable unavailable") {
		t.Fatalf("status=%d stderr=%q; want trusted executable failure", status, stderr.String())
	}
}

// TestProductionSessionQueryKeepsAgentCredentials proves that tmux session queries request 1000:1000 without privileged setgroups or caller input through the production exec.Cmd adapter seam.
func TestProductionSessionQueryKeepsAgentCredentials(t *testing.T) {
	var calls int
	effects := newEffects(systemCalls{
		Identity:    func() Identity { return Identity{UID: 1000, GID: 1000} },
		Executable:  func() (string, error) { t.Error("worker tried to re-exec"); return "", nil },
		Environment: func() []string { return []string{"HOME=/root", "KEEP=value"} },
		Start: func(command *exec.Cmd) error {
			calls++
			if !reflect.DeepEqual(command.Args, []string{"tmux", "-L", "sandboxed-agents", "-f", "/dev/null", "list-sessions", "-F", "#{session_name}"}) {
				t.Errorf("tmux arguments=%q", command.Args)
			}
			assertAgentCredentials(t, command, true)
			if command.Stdin != nil {
				t.Error("tmux session query inherits caller input")
			}
			return nil
		},
	})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"__worker", "sessions"}, io.Discard, io.Discard, effects); status != 0 || calls != 1 {
		t.Fatalf("status=%d starts=%d; want one unprivileged tmux query", status, calls)
	}
}

// TestProductionGitConfigUsesOnlyAgentHome proves sandbox Git overrides cannot redirect global config writes outside home and Git keeps explicit agent credentials through the production process seam.
func TestProductionGitConfigUsesOnlyAgentHome(t *testing.T) {
	starts := 0
	effects := newEffects(systemCalls{
		Identity: func() Identity { return Identity{UID: 1000, GID: 1000} },
		Environment: func() []string {
			return []string{"HOME=/outside", "GIT_CONFIG_GLOBAL=/outside/gitconfig", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=bad", "XDG_CONFIG_HOME=/outside/xdg", "PATH=/usr/bin"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			assertAgentCredentials(t, command, true)
			values := map[string]string{}
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			if values["HOME"] != "/home/agent" || values["GIT_CONFIG_GLOBAL"] != "/home/agent/.gitconfig" || values["XDG_CONFIG_HOME"] != "/home/agent/.config" {
				t.Errorf("environment=%q; want Git writes confined to home", command.Env)
			}
			for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
				if _, exists := values[key]; exists {
					t.Errorf("inherited config override %s", key)
				}
			}
			return nil
		},
	})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"integrations", "config", "git", "credentials"}, io.Discard, io.Discard, effects); status != 0 || starts != 1 {
		t.Fatalf("status=%d starts=%d", status, starts)
	}
}

// TestProductionAgentAdministrationDropsRootBeforeHome proves administrative agent calls request the trusted worker with UID/GID 1000 and cleared groups before home access through the production exec.Cmd seam.
func TestProductionAgentAdministrationDropsRootBeforeHome(t *testing.T) {
	for _, args := range [][]string{{"agents", "list"}, {"agents", "enable", "codex"}, {"agents", "status", "codex"}, {"agents", "disable", "codex"}, {"agents", "update", "codex"}} {
		starts := 0
		effects := newEffects(systemCalls{
			Identity:    func() Identity { return rootIdentity },
			Executable:  func() (string, error) { return "/trusted/manager", nil },
			Environment: func() []string { return []string{"HOME=/root", "USER=root"} },
			Start: func(command *exec.Cmd) error {
				starts++
				assertAgentCredentials(t, command, false)
				want := append([]string{"/trusted/manager", "__worker"}, args...)
				if command.Path != "/trusted/manager" || !reflect.DeepEqual(command.Args, want) {
					t.Errorf("worker=%q want=%q", command.Args, want)
				}
				return nil
			},
		})
		effects.HomeDirectory = func() (string, error) { t.Fatal("administrative root read home"); return "", nil }
		if status := RunWithEffects(args, io.Discard, io.Discard, effects); status != 0 || starts != 1 {
			t.Fatalf("args=%q status=%d starts=%d", args, status, starts)
		}
	}
}

// TestProductionNpmUsesAgentHomeAndCredentials proves npm gets the isolated prefix, explicit worker credentials, and home-confined npm config and cache through RunWithEffects and the production exec.Cmd seam.
func TestProductionNpmUsesAgentHomeAndCredentials(t *testing.T) {
	home, starts := t.TempDir(), 0
	effects := newEffects(systemCalls{
		Identity: func() Identity { return agentIdentity },
		Environment: func() []string {
			return []string{"HOME=/outside", "NPM_CONFIG_PREFIX=/outside/prefix", "npm_config_cache=/outside/cache", "NPM_CONFIG_USERCONFIG=/outside/npmrc", "PATH=/usr/bin"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			assertAgentCredentials(t, command, true)
			values := map[string]string{}
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			if values["HOME"] != "/home/agent" || values["NPM_CONFIG_CACHE"] != "/home/agent/.npm" || values["NPM_CONFIG_USERCONFIG"] != "/home/agent/.npmrc" {
				t.Errorf("npm environment=%v", values)
			}
			for _, key := range []string{"npm_config_cache", "NPM_CONFIG_PREFIX"} {
				if _, exists := values[key]; exists {
					t.Errorf("inherited npm override %s", key)
				}
			}
			if len(command.Args) != 7 || command.Args[1] != "install" || command.Args[2] != "--global" || command.Args[3] != "--prefix" || command.Args[5] != "--" || command.Args[6] != "@openai/codex" {
				t.Fatalf("npm args=%q", command.Args)
			}
			prefix := command.Args[4]
			if !strings.HasPrefix(prefix, home+"/") {
				t.Fatalf("npm prefix outside temporary home=%s", prefix)
			}
			packagePath := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex")
			if err := os.MkdirAll(packagePath, 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(`{"version":"1.2.3"}`), 0600); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(prefix, "bin", "codex"), []byte("command"), 0700)
		},
	})
	effects.HomeDirectory = func() (string, error) { return home, nil }
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"agents", "enable", "codex"}, io.Discard, io.Discard, effects); status != 0 || starts != 1 {
		t.Fatalf("status=%d starts=%d", status, starts)
	}
}

// TestProductionGitHubLoginKeepsInputAndHome proves the device flow receives terminal input and confines gh and Git credentials to agent's home through the production exec.Cmd seam.
func TestProductionGitHubLoginKeepsInputAndHome(t *testing.T) {
	starts := 0
	effects := newEffects(systemCalls{
		Identity: func() Identity { return agentIdentity },
		Environment: func() []string {
			return []string{"HOME=/outside", "GH_CONFIG_DIR=/outside/gh", "GH_TOKEN=ambient", "GITHUB_TOKEN=ambient", "GIT_CONFIG_GLOBAL=/outside/git", "XDG_CONFIG_HOME=/outside", "PATH=/usr/bin"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			assertAgentCredentials(t, command, true)
			if command.Stdin != os.Stdin {
				t.Error("login lost terminal stdin")
			}
			values := map[string]string{}
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			for key, want := range map[string]string{"HOME": "/home/agent", "GH_CONFIG_DIR": "/home/agent/.config/gh", "GIT_CONFIG_GLOBAL": "/home/agent/.gitconfig", "XDG_CONFIG_HOME": "/home/agent/.config"} {
				if values[key] != want {
					t.Errorf("%s=%q want=%q", key, values[key], want)
				}
			}
			for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
				if _, ok := values[key]; ok {
					t.Errorf("ambient %s bypasses device login", key)
				}
			}
			return nil
		},
	})
	stubPassingAgentIDReads(&effects)
	if status := RunWithEffects([]string{"integrations", "login", "github"}, io.Discard, io.Discard, effects); status != 0 || starts != 2 {
		t.Fatalf("status=%d starts=%d", status, starts)
	}
}

// TestProductionAzureDevOpsKeepsSecretsInHome proves token subprocesses have explicit agent identity, fixed home storage, and no ambient authentication or trace overrides through the production exec.Cmd seam.
func TestProductionAzureDevOpsKeepsSecretsInHome(t *testing.T) {
	starts := 0
	effects := newEffects(systemCalls{
		Identity: func() Identity { return agentIdentity },
		Environment: func() []string {
			return []string{"HOME=/outside", "AZURE_CONFIG_DIR=/outside", "AZURE_DEVOPS_EXT_PAT=ambient", "AZURE_CORE_LOG_LEVEL=debug", "AZURE_LOGGING_ENABLE_LOG_FILE=true", "GIT_CONFIG_GLOBAL=/outside/git", "GIT_TRACE=/outside/trace", "GIT_CURL_VERBOSE=1", "PATH=/usr/bin"}
		},
		Start: func(command *exec.Cmd) error {
			starts++
			assertAgentCredentials(t, command, true)
			values := map[string]string{}
			for _, entry := range command.Env {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			for key, want := range map[string]string{"HOME": "/home/agent", "AZURE_CONFIG_DIR": "/home/agent/.azure", "GIT_CONFIG_GLOBAL": "/home/agent/.gitconfig", "AZURE_LOGGING_ENABLE_LOG_FILE": "false", "AZURE_CORE_LOG_LEVEL": "critical"} {
				if values[key] != want {
					t.Errorf("%s=%q want=%q", key, values[key], want)
				}
			}
			for _, key := range []string{"AZURE_DEVOPS_EXT_PAT", "GIT_TRACE", "GIT_CURL_VERBOSE"} {
				if _, ok := values[key]; ok {
					t.Errorf("unsafe environment %s", key)
				}
			}
			if strings.Contains(strings.Join(command.Args, " "), "runtime-pat-sentinel") {
				t.Fatal("token in arguments")
			}
			return nil
		},
	})
	stubPassingAgentIDReads(&effects)
	effects.Stdin = strings.NewReader("runtime-pat-sentinel\n")
	if status := RunWithEffects([]string{"integrations", "login", "azdo", "--organization", "https://dev.azure.com/contoso"}, io.Discard, io.Discard, effects); status != 0 || starts != 4 {
		t.Fatalf("status=%d starts=%d", status, starts)
	}
}

// TestProductionAzureLoginKeepsHomeAndBrowserCallback proves Azure credentials stay in home and browser launch uses the manager callback through the production exec.Cmd seam.
func TestProductionAzureLoginKeepsHomeAndBrowserCallback(t *testing.T) {
	for _, workflow := range []string{"device-code", "browser"} {
		starts := 0
		effects := newEffects(systemCalls{
			Identity:   func() Identity { return agentIdentity },
			Executable: func() (string, error) { return "/usr/local/bin/sandboxed-agents-manager", nil },
			Environment: func() []string {
				return []string{"HOME=/outside", "AZURE_CONFIG_DIR=/outside/azure", "BROWSER=ambient-browser", "PATH=/usr/bin"}
			},
			Start: func(command *exec.Cmd) error {
				starts++
				assertAgentCredentials(t, command, true)
				values := map[string]string{}
				for _, entry := range command.Env {
					key, value, _ := strings.Cut(entry, "=")
					values[key] = value
				}
				if values["AZURE_CONFIG_DIR"] != "/home/agent/.azure" {
					t.Errorf("config=%q", values["AZURE_CONFIG_DIR"])
				}
				if workflow == "browser" && values["BROWSER"] != "'/usr/local/bin/sandboxed-agents-manager' __azure-browser agent01 %s" {
					t.Errorf("browser=%q", values["BROWSER"])
				}
				if workflow == "device-code" && values["BROWSER"] != "" {
					t.Errorf("ambient browser=%q", values["BROWSER"])
				}
				if command.Stdin != os.Stdin {
					t.Error("login lost terminal input")
				}
				return nil
			},
		})
		stubPassingAgentIDReads(&effects)
		args := []string{"integrations", "login", "azure", workflow}
		if workflow == "browser" {
			args = append(args, "--ssh-host", "agent01")
		}
		if status := RunWithEffects(args, io.Discard, io.Discard, effects); status != 0 || starts != 2 {
			t.Fatalf("status=%d starts=%d", status, starts)
		}
	}
}

// TestProductionAzureBrowserUsesRelocatedManager proves Python's real browser adapter launches the running manager path with literal arguments through the production environment seam.
func TestProductionAzureBrowserUsesRelocatedManager(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python browser adapter is unavailable")
	}
	callback := filepath.Join(t.TempDir(), "manager's callback")
	if err := os.WriteFile(callback, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	address := "https://login.example/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A43123&state=literal"
	starts := 0
	effects := newEffects(systemCalls{
		Identity:    func() Identity { return agentIdentity },
		Executable:  func() (string, error) { return callback, nil },
		Environment: os.Environ,
		Start: func(command *exec.Cmd) error {
			starts++
			if command.Args[1] != "login" {
				return nil
			}
			browser := exec.Command(python, "-c", "import sys,webbrowser; sys.exit(0 if webbrowser.open(sys.argv[1]) else 1)", address)
			browser.Env = command.Env
			output, err := browser.CombinedOutput()
			want := "__azure-browser\nagent01\n" + address + "\n"
			if err != nil || string(output) != want {
				t.Errorf("browser output=%q err=%v want=%q", output, err, want)
			}
			return err
		},
	})
	stubPassingAgentIDReads(&effects)
	var out bytes.Buffer
	if status := RunWithEffects([]string{"integrations", "login", "azure", "browser", "--ssh-host", "agent01"}, &out, &out, effects); status != 0 || starts != 2 {
		t.Fatalf("status=%d starts=%d output=%s", status, starts, &out)
	}
}

// TestProductionAzureBrowserRejectsUnusableExecutable proves callback path lookup and unrepresentable BROWSER paths fail before Azure starts through the production process seam.
func TestProductionAzureBrowserRejectsUnusableExecutable(t *testing.T) {
	for _, tc := range []struct {
		path string
		err  error
	}{{"", errors.New("executable unavailable")}, {"/manager:other", nil}, {"/manager%s", nil}, {"", nil}} {
		effects := newEffects(systemCalls{
			Identity:    func() Identity { return agentIdentity },
			Executable:  func() (string, error) { return tc.path, tc.err },
			Environment: func() []string { return nil },
			Start:       func(*exec.Cmd) error { t.Fatal("Azure started without a usable browser callback"); return nil },
		})
		stubPassingAgentIDReads(&effects)
		var out bytes.Buffer
		if status := RunWithEffects([]string{"integrations", "login", "azure", "browser", "--ssh-host", "agent01"}, &out, &out, effects); status != 1 {
			t.Fatalf("path=%q status=%d output=%s", tc.path, status, &out)
		}
	}
}
