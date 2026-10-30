package manager_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// sshConnectionSkip skips a connection flow test where the local OpenSSH client that answers the alias unit's ssh -G queries is missing.
func sshConnectionSkip(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("the local OpenSSH client is not installed")
	}
}

// sshFlowEffects extends sshKeyEffects for the whole SSH workflow: input is the user's terminal input, the alias unit's ssh -G
// queries run the local OpenSSH client, which only resolves configuration, and every other /usr/bin/ssh call, the connection test,
// goes to connect instead of any network. Each ssh call is recorded in calls after the key processes.
func sshFlowEffects(t *testing.T, home string, input io.Reader, connect func(manager.Process) error, calls *[]manager.Process) manager.Effects {
	t.Helper()
	sshConnectionSkip(t)
	e := sshKeyEffects(t, home, calls)
	e.Stdin = input
	keygen := e.Start
	e.Start = func(p manager.Process) error {
		if p.Program != "/usr/bin/ssh" {
			return keygen(p)
		}
		*calls = append(*calls, p)
		if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || p.EnvironmentPolicy != manager.SSHEnvironment || len(p.Environment) != 0 {
			t.Fatalf("ssh not run as agent with the fixed SSH environment: %+v", p)
		}
		if !slices.Contains(p.Args, "-G") {
			return connect(p)
		}
		c := exec.Command(p.Program, p.Args...)
		c.Stdin, c.Stdout, c.Stderr = p.Stdin, p.Stdout, p.Stderr
		c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
		return c.Run()
	}
	return e
}

// connectionTests returns the recorded ssh calls that are connection tests rather than ssh -G configuration queries.
func connectionTests(calls []manager.Process) []manager.Process {
	var tests []manager.Process
	for _, p := range calls {
		if p.Program == "/usr/bin/ssh" && !slices.Contains(p.Args, "-G") {
			tests = append(tests, p)
		}
	}
	return tests
}

// exitStatus returns the error a real child process reports for the exit status, as Effects.Start returns it.
func exitStatus(t *testing.T, status int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(status)).Run()
	if err == nil && status != 0 {
		t.Fatalf("exit %d reported no error", status)
	}
	return err
}

// connectWith returns a connection test fake that writes diagnostics to the test's standard error and returns err.
func connectWith(diagnostics string, err error) func(manager.Process) error {
	return func(p manager.Process) error {
		io.WriteString(p.Stderr, diagnostics)
		return err
	}
}

// runSSHFlow runs the SSH workflow through RunWithEffects and returns its status, standard output, and diagnostics.
func runSSHFlow(e manager.Effects, host, user string) (int, string, string) {
	var out, diag bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", host, "--user", user}, &out, &diag, e)
	return status, out.String(), diag.String()
}

// TestSSHConnectionAsksApprovalBeforeConnecting proves through RunWithEffects that the approval question follows the printed public
// key and precedes the connection test, so no ssh connection starts before the user approves.
func TestSSHConnectionAsksApprovalBeforeConnecting(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	var out, diag bytes.Buffer
	connect := func(p manager.Process) error {
		if !strings.HasPrefix(out.String(), "ssh-ed25519 ") {
			t.Fatalf("connection test before the public key was printed: %q", out.String())
		}
		if !strings.Contains(diag.String(), "Test the SSH connection to alice@worker with password authentication now?") {
			t.Fatalf("connection test before the approval question: %q", diag.String())
		}
		return nil
	}
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), connect, &calls)
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", "worker", "--user", "alice"}, &out, &diag, e)
	if status != 0 || len(connectionTests(calls)) != 1 {
		t.Fatalf("status=%d tests=%d diagnostics=%s", status, len(connectionTests(calls)), &diag)
	}
}

// TestSSHConnectionDeclineOrCancelConnectsNothing proves through RunWithEffects that answering no, an empty answer, end of input, or
// Ctrl+C at the approval question starts no connection test, writes no SSH configuration, names the outcome and the retained
// dedicated key, and exits non-zero.
func TestSSHConnectionDeclineOrCancelConnectsNothing(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		interrupt         bool
	}{
		{"no", "no\n", "SSH connection test declined", false},
		{"empty answer", "\n", "SSH connection test declined", false},
		{"other answer", "maybe\n", "SSH connection test declined", false},
		{"end of input", "", "SSH connection test cancelled", false},
		{"Ctrl+C", "", "SSH connection test cancelled", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			var calls []manager.Process
			var input io.Reader = strings.NewReader(test.input)
			if test.interrupt {
				// The terminal stays open while Ctrl+C arrives; the question must not wait for an answer.
				reader, writer := io.Pipe()
				t.Cleanup(func() { writer.Close() })
				input = reader
			}
			e := sshFlowEffects(t, home, input, func(p manager.Process) error {
				t.Fatalf("connection test without approval: %v", p.Args)
				return nil
			}, &calls)
			if test.interrupt {
				e.NotifyInterrupt = func() (<-chan os.Signal, func()) {
					interrupts := make(chan os.Signal, 1)
					interrupts <- syscall.SIGINT
					return interrupts, func() {}
				}
			}
			status, out, diag := runSSHFlow(e, "worker", "alice")
			if status != 1 || !strings.HasPrefix(out, "ssh-ed25519 ") || !strings.Contains(diag, test.want) {
				t.Fatalf("status=%d output=%q diagnostics=%q", status, out, diag)
			}
			keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(keys) != 2 || !strings.Contains(diag, "dedicated SSH key retained at "+keys[0]) {
				t.Fatalf("keys=%q diagnostics=%q", keys, diag)
			}
			if !strings.Contains(diag, "no connection made") || !strings.Contains(diag, "no SSH host entry written") {
				t.Fatalf("outcome not explained: %q", diag)
			}
			for _, name := range []string{"config", "known_hosts"} {
				if _, err := os.Lstat(filepath.Join(home, ".ssh", name)); !os.IsNotExist(err) {
					t.Fatalf("%s written: %v", name, err)
				}
			}
		})
	}
}

// TestSSHConnectionTestUsesPasswordAuthenticationOnly proves through RunWithEffects that the connection test runs the trusted ssh client
// as agent with the fixed SSH environment, ignores every SSH configuration file, allows password authentication only, so no key
// login is attempted and no key is offered, makes ssh ask for confirmation of an unknown host key and store only confirmed keys in
// the agent's known_hosts, and carries no password in its arguments, environment, or standard input: ssh reads it from the terminal.
func TestSSHConnectionTestUsesPasswordAuthenticationOnly(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), connectWith("", nil), &calls)
	status, _, diag := runSSHFlow(e, "worker", `DOMAIN\alice`)
	tests := connectionTests(calls)
	if status != 0 || len(tests) != 1 {
		t.Fatalf("status=%d tests=%d diagnostics=%s", status, len(tests), diag)
	}
	want := []string{"-n", "-T", "-F", "/dev/null",
		"-o", "BatchMode=no",
		"-o", "ConnectTimeout=15",
		"-o", "ConnectionAttempts=1",
		"-o", "PreferredAuthentications=password",
		"-o", "PasswordAuthentication=yes",
		"-o", "PubkeyAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "HostbasedAuthentication=no",
		"-o", "GSSAPIAuthentication=no",
		"-o", "IdentityAgent=none",
		"-o", "StrictHostKeyChecking=ask",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts"),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "UpdateHostKeys=no",
		"-o", "CheckHostIP=no",
		"-o", "VerifyHostKeyDNS=no",
		"-o", "HashKnownHosts=no",
		"-o", "ClearAllForwardings=yes",
		"-l", `DOMAIN\alice`, "--", "worker", "exit 0"}
	test := tests[0]
	if !slices.Equal(test.Args, want) {
		t.Fatalf("args=%q\nwant=%q", test.Args, want)
	}
	if slices.ContainsFunc(test.Args, func(arg string) bool {
		return strings.Contains(arg, "IdentityFile") || strings.Contains(arg, "id_ed25519")
	}) {
		t.Fatalf("connection test offers a key: %q", test.Args)
	}
	if test.Stdin == nil || test.Stdin == e.Stdin {
		t.Fatal("connection test reads the user's terminal input through standard input instead of ssh's own terminal prompt")
	}
	if data, _ := io.ReadAll(test.Stdin); len(data) != 0 {
		t.Fatalf("connection test standard input carries %q", data)
	}
	if test.Stdout == nil || test.Stderr == nil {
		t.Fatal("connection test streams not attached")
	}
}

// TestSSHConnectionFailuresNameTheirCause proves through RunWithEffects that each recorded ssh failure, an unresolvable name, an
// unreachable or silent machine, a host key the user rejected or one that changed, and a refused password, produces a message naming
// its cause, as does an ssh that cannot start, exits non-zero, leaves an existing ~/.ssh/config byte for byte unchanged, writes no host entry, and names the retained key.
func TestSSHConnectionFailuresNameTheirCause(t *testing.T) {
	for _, test := range []struct {
		name, diagnostics, want string
		status                  int
	}{
		{"name not resolvable", "ssh: Could not resolve hostname worker: Name or service not known\r\n", "name not resolvable", 255},
		{"temporary resolver failure", "ssh: Could not resolve hostname worker: Temporary failure in name resolution\r\n", "name not resolvable", 255},
		{"timed out", "ssh: connect to host worker port 22: Connection timed out\r\n", "unreachable or timed out", 255},
		{"refused", "ssh: connect to host worker port 22: Connection refused\r\n", "unreachable or timed out", 255},
		{"no route", "ssh: connect to host worker port 22: No route to host\r\n", "unreachable or timed out", 255},
		{"network unreachable", "ssh: connect to host worker port 22: Network is unreachable\r\n", "unreachable or timed out", 255},
		{"host key rejected", "Host key verification failed.\r\n", "host key rejected", 255},
		{"host key changed", "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
			"Host key for worker has changed and you have requested strict checking.\r\nHost key verification failed.\r\n", "host key rejected: the host key of worker differs from the one trusted", 255},
		{"authentication failed", "Permission denied, please try again.\r\nalice@worker: Permission denied (password).\r\n", "authentication failed", 255},
		{"password method not offered", "alice@worker: Permission denied (publickey).\r\n", "authentication failed", 255},
		{"other ssh failure", "kex_exchange_identification: Connection closed by remote host\r\n", "SSH connection test failed", 255},
		{"ssh cannot start", "", "ssh could not start", -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			existing := "# kept by hand\nHost other\n    User bob\n"
			config := filepath.Join(home, ".ssh", "config")
			if err := os.WriteFile(config, []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			var calls []manager.Process
			failure := errors.New("fork/exec /usr/bin/ssh: no such file or directory")
			if test.status >= 0 {
				failure = exitStatus(t, test.status)
			}
			e := sshFlowEffects(t, home, strings.NewReader("yes\nlab\n"), connectWith(test.diagnostics, failure), &calls)
			status, _, diag := runSSHFlow(e, "worker", "alice")
			if status != 1 || !strings.Contains(diag, test.want) {
				t.Fatalf("status=%d diagnostics=%q; want %q", status, diag, test.want)
			}
			if !strings.Contains(diag, "no SSH host entry written") || !strings.Contains(diag, "dedicated SSH key retained at ") {
				t.Fatalf("outcome not explained: %q", diag)
			}
			if got, err := os.ReadFile(config); err != nil || string(got) != existing {
				t.Fatalf("config changed: %q %v", got, err)
			}
			for _, p := range calls {
				if p.Program == "/usr/bin/ssh" && slices.Contains(p.Args, "-G") {
					t.Fatalf("alias unit ran after a failed test: %q", p.Args)
				}
			}
		})
	}
}

// TestSSHConnectionInterruptedTestWritesNothing proves through RunWithEffects that Ctrl+C during the connection test, at ssh's host
// key or password prompt, ends the workflow as cancelled, non-zero, with no host entry.
func TestSSHConnectionInterruptedTestWritesNothing(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	interrupts := make(chan os.Signal, 1)
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), func(manager.Process) error {
		interrupts <- syscall.SIGINT
		return exitStatus(t, 255)
	}, &calls)
	e.NotifyInterrupt = func() (<-chan os.Signal, func()) { return interrupts, func() {} }
	status, _, diag := runSSHFlow(e, "worker", "alice")
	if status != 1 || !strings.Contains(diag, "SSH connection test cancelled") || !strings.Contains(diag, "no SSH host entry written") {
		t.Fatalf("status=%d diagnostics=%q", status, diag)
	}
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("config written: %v", err)
	}
}

// TestSSHConnectionKeepsConfirmedHostKeyAfterFailedPassword proves through RunWithEffects that a host key ssh stored after the user
// confirmed it stays in the agent's known_hosts when the password is then refused, as ADR-0011 decides.
func TestSSHConnectionKeepsConfirmedHostKeyAfterFailedPassword(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	trusted := "worker ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), func(p manager.Process) error {
		if err := os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(trusted), 0600); err != nil {
			t.Fatal(err)
		}
		io.WriteString(p.Stderr, "Warning: Permanently added 'worker' (ED25519) to the list of known hosts.\r\nalice@worker: Permission denied (password).\r\n")
		return exitStatus(t, 255)
	}, &calls)
	status, _, diag := runSSHFlow(e, "worker", "alice")
	if status != 1 || !strings.Contains(diag, "authentication failed") {
		t.Fatalf("status=%d diagnostics=%q", status, diag)
	}
	if got, err := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); err != nil || string(got) != trusted {
		t.Fatalf("confirmed host key not kept: %q %v", got, err)
	}
}

// TestSSHConnectionSuccessAddsAliasEntry proves through RunWithEffects that a successful connection test, including one whose remote
// command reports its own non-zero status after authentication, runs the alias unit, which for an address asks for the alias and
// writes its host entry, and exits zero with a message that the password test does not prove the dedicated key is authorized.
func TestSSHConnectionSuccessAddsAliasEntry(t *testing.T) {
	for _, remote := range []int{0, 1} {
		t.Run("remote status "+strconv.Itoa(remote), func(t *testing.T) {
			home := t.TempDir()
			var calls []manager.Process
			e := sshFlowEffects(t, home, strings.NewReader("yes\nlab\n"), connectWith("", exitStatus(t, remote)), &calls)
			status, out, diag := runSSHFlow(e, "192.0.2.7", "alice")
			if status != 0 || strings.Count(out, "\n") != 1 {
				t.Fatalf("status=%d output=%q diagnostics=%q", status, out, diag)
			}
			config, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
			if err != nil || !strings.HasPrefix(string(config), "Host lab\n    HostName 192.0.2.7\n    User alice\n") {
				t.Fatalf("config=%q error=%v", config, err)
			}
			if !strings.Contains(diag, "SSH alias lab added") || !strings.Contains(diag, "does not prove that the dedicated key is authorized") {
				t.Fatalf("diagnostics=%q", diag)
			}
		})
	}
}

// TestSSHConnectionAliasFailureAfterSuccessExitsNonZero proves through RunWithEffects that when the alias question is cancelled after
// a successful connection test, the workflow writes no entry, names the retained key, and exits non-zero.
func TestSSHConnectionAliasFailureAfterSuccessExitsNonZero(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), connectWith("", nil), &calls)
	status, _, diag := runSSHFlow(e, "192.0.2.7", "alice")
	if status != 1 || !strings.Contains(diag, "no SSH alias name given") || !strings.Contains(diag, "dedicated SSH key retained at ") {
		t.Fatalf("status=%d diagnostics=%q", status, diag)
	}
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("config written: %v", err)
	}
}

// TestSSHConnectionOptionsResolveThroughRealOpenSSH proves with the local OpenSSH client, where installed, that the recorded connection
// test arguments are accepted and resolve to password authentication as the only method, no key, agent, or host-based login, a host
// key confirmation prompt, the agent's known_hosts as the only trust store, and no silent host key updates. It replays the arguments
// that RunWithEffects passed to the connection test with ssh -G prepended, which only resolves configuration and connects nowhere.
func TestSSHConnectionOptionsResolveThroughRealOpenSSH(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	e := sshFlowEffects(t, home, strings.NewReader("yes\n"), connectWith("", nil), &calls)
	if status, _, diag := runSSHFlow(e, "worker", "alice"); status != 0 {
		t.Fatalf("status=%d diagnostics=%s", status, diag)
	}
	resolve := exec.Command("/usr/bin/ssh", append([]string{"-G"}, connectionTests(calls)[0].Args...)...)
	resolve.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
	output, err := resolve.Output()
	if err != nil {
		t.Fatalf("ssh -G refused the connection test arguments: %v", err)
	}
	resolved := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok {
			resolved[strings.ToLower(key)] = value
		}
	}
	for key, want := range map[string]string{
		"user":                         "alice",
		"hostname":                     "worker",
		"preferredauthentications":     "password",
		"passwordauthentication":       "yes",
		"pubkeyauthentication":         "false",
		"kbdinteractiveauthentication": "no",
		"hostbasedauthentication":      "no",
		"gssapiauthentication":         "no",
		"identityagent":                "none",
		"stricthostkeychecking":        "ask",
		"userknownhostsfile":           filepath.Join(home, ".ssh", "known_hosts"),
		"globalknownhostsfile":         "/dev/null",
		"updatehostkeys":               "false",
		"batchmode":                    "no",
		"connecttimeout":               "15",
	} {
		if resolved[key] != want {
			t.Errorf("%s resolves to %q; want %q", key, resolved[key], want)
		}
	}
}
