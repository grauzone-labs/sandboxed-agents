package manager_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// sshFlowEvent records, in order, what the SSH integration did at the manager seam: printed output, read an answer, or
// started a program.
type sshFlowEvent struct {
	kind    string
	process manager.Process
}

// sshFlowRecorder appends an event for every write to a stream or read from the answers, so tests can order the
// approval question against output and process starts.
type sshFlowRecorder struct {
	events *[]sshFlowEvent
	kind   string
	target io.ReadWriter
}

// Read records one answer read and returns bytes from the scripted answers.
func (r sshFlowRecorder) Read(data []byte) (int, error) {
	*r.events = append(*r.events, sshFlowEvent{kind: r.kind})
	return r.target.Read(data)
}

// Write records one write and keeps the bytes for later inspection.
func (r sshFlowRecorder) Write(data []byte) (int, error) {
	*r.events = append(*r.events, sshFlowEvent{kind: r.kind})
	return r.target.Write(data)
}

// runSSHFlow runs the SSH integration with scripted answers on standard input and returns its status, standard output,
// diagnostics, and the ordered events of public output, answer reads, and process starts. connect answers every process
// the flow starts other than /usr/bin/ssh-keygen, which covers the connection test and the alias unit's ssh -G queries;
// /usr/bin/ssh-keygen runs locally as sshKeyEffects does, and with a nil connect any other process fails the test.
func runSSHFlow(t *testing.T, home, host, user, answers string, connect func(manager.Process) error) (int, string, string, []sshFlowEvent) {
	t.Helper()
	var calls []manager.Process
	var events []sshFlowEvent
	e := sshKeyEffects(t, home, &calls)
	keygen := e.Start
	e.Start = func(p manager.Process) error {
		events = append(events, sshFlowEvent{kind: "start", process: p})
		if p.Program == "/usr/bin/ssh-keygen" {
			return keygen(p)
		}
		if connect == nil {
			t.Fatalf("unexpected process: %+v", p)
		}
		return connect(p)
	}
	var out, diag bytes.Buffer
	e.Stdin = sshFlowRecorder{events: &events, kind: "answer", target: bytes.NewBufferString(answers)}
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", host, "--user", user}, sshFlowRecorder{events: &events, kind: "public", target: &out}, &diag, e)
	return status, out.String(), diag.String(), events
}

// TestSSHIntegrationAsksApprovalBeforeAnyConnection proves the flow prints the public key, then asks for approval of the
// connection test, that an unrecognised answer is asked again, and that declining, an empty answer, and end of input make
// no connection, write no host entry, retain the dedicated key, say so, and exit non-zero, through RunWithEffects with
// scripted answers and recorded process starts.
func TestSSHIntegrationAsksApprovalBeforeAnyConnection(t *testing.T) {
	for _, c := range []struct{ name, answers, says string }{
		{"no", "no\n", "SSH connection test declined"},
		{"n", "N\n", "SSH connection test declined"},
		{"empty", "\n", "SSH connection test declined"},
		{"reasked", "maybe\nno\n", "SSH connection test declined"},
		{"end of input", "", "SSH connection test cancelled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			status, out, diag, events := runSSHFlow(t, home, "build.example", "alice", c.answers, nil)
			if status != 1 || !strings.HasPrefix(out, "ssh-ed25519 ") {
				t.Fatalf("status=%d out=%q diagnostics=%s", status, out, diag)
			}
			if !strings.Contains(diag, "alice@build.example") || !strings.Contains(diag, c.says) || !strings.Contains(diag, "no connection made") || !strings.Contains(diag, "no host entry written") {
				t.Fatalf("diagnostics do not name the question and its outcome: %s", diag)
			}
			if c.name == "reasked" && strings.Count(diag, "alice@build.example with password authentication") != 2 {
				t.Fatalf("unrecognized answer was not asked again: %s", diag)
			}
			printed, asked := -1, -1
			for i, event := range events {
				switch {
				case event.kind == "public" && printed < 0:
					printed = i
				case event.kind == "answer" && asked < 0:
					asked = i
				case event.kind == "start" && asked >= 0:
					t.Fatalf("process started after the approval question: %+v", event.process)
				}
			}
			if printed < 0 || asked < printed {
				t.Fatalf("approval asked before the public key was printed: %+v", events)
			}
			keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(keys) != 2 || !strings.Contains(diag, "dedicated key retained at "+strings.TrimSuffix(keys[0], ".pub")) {
				t.Fatalf("keys=%q diagnostics=%s", keys, diag)
			}
			for _, name := range []string{"config", "known_hosts"} {
				if _, err := os.Lstat(filepath.Join(home, ".ssh", name)); !os.IsNotExist(err) {
					t.Fatalf("%s written after %s: %v", name, c.name, err)
				}
			}
		})
	}
}

// sshInterruptingInput is standard input at the approval question that delivers Ctrl+C on the fake interrupt channel
// when the question first reads and then blocks until released, as a terminal does while the user has not answered; a
// bounded wait keeps a flow that ignores the interrupt from hanging the test.
type sshInterruptingInput struct {
	interrupts chan<- os.Signal
	released   <-chan struct{}
	reads      *int
}

// Read sends one interrupt on the first read, then waits until the test releases the input or ten seconds pass and
// reports end of input.
func (r sshInterruptingInput) Read([]byte) (int, error) {
	*r.reads++
	if *r.reads == 1 {
		r.interrupts <- os.Interrupt
	}
	select {
	case <-r.released:
	case <-time.After(10 * time.Second):
	}
	return 0, io.EOF
}

// TestSSHIntegrationCancelsOnInterruptAtApproval proves Ctrl+C while the approval question waits for an answer makes no
// connection, writes no host entry, says so with the retained key's location, and exits non-zero, and that the manager
// catches the interrupt only around the question, through RunWithEffects with the fake NotifyInterrupt seam and standard
// input that interrupts while the question reads.
func TestSSHIntegrationCancelsOnInterruptAtApproval(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	e := sshKeyEffects(t, home, &calls)
	keygen := e.Start
	e.Start = func(p manager.Process) error {
		if p.Program != "/usr/bin/ssh-keygen" {
			t.Fatalf("process started after an interrupt at the approval question: %+v", p)
		}
		return keygen(p)
	}
	interrupts := make(chan os.Signal, 1)
	watches, restores := 0, 0
	e.NotifyInterrupt = func() (<-chan os.Signal, func()) {
		watches++
		return interrupts, func() { restores++ }
	}
	released := make(chan struct{})
	defer close(released)
	reads := 0
	e.Stdin = sshInterruptingInput{interrupts: interrupts, released: released, reads: &reads}
	var out, diag bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", "build.example", "--user", "alice"}, &out, &diag, e)
	if status != 1 || !strings.HasPrefix(out.String(), "ssh-ed25519 ") || reads != 1 {
		t.Fatalf("status=%d reads=%d out=%q diagnostics=%s", status, reads, out.String(), diag.String())
	}
	if watches != 1 || restores != 1 {
		t.Fatalf("interrupt watches=%d restores=%d, want one each around the question", watches, restores)
	}
	keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
	if len(keys) != 2 {
		t.Fatalf("dedicated key not retained: %q", keys)
	}
	want := "SSH connection test cancelled; no connection made, no host entry written, dedicated key retained at " + strings.TrimSuffix(keys[0], ".pub") + "\n"
	if !strings.Contains(diag.String(), "alice@build.example with password authentication") || !strings.HasSuffix(diag.String(), want) {
		t.Fatalf("diagnostics do not end with %q: %s", want, diag.String())
	}
	for _, name := range []string{"config", "known_hosts"} {
		if _, err := os.Lstat(filepath.Join(home, ".ssh", name)); !os.IsNotExist(err) {
			t.Fatalf("%s written after an interrupt: %v", name, err)
		}
	}
}

// TestSSHIntegrationCancelsOnInterruptDuringTest proves Ctrl+C during the connection test cancels the flow even when ssh
// then exits with a status other than 255, which would otherwise count as a login: the flow says the test was cancelled
// with the after-attempt consequences, queries no alias, writes no SSH configuration, exits non-zero, and catches the
// interrupt only around the question and the test, through RunWithEffects with the fake NotifyInterrupt seam and a fake
// Start whose connection test delivers the interrupt and returns exit status 1.
func TestSSHIntegrationCancelsOnInterruptDuringTest(t *testing.T) {
	home := t.TempDir()
	var calls []manager.Process
	e := sshKeyEffects(t, home, &calls)
	keygen := e.Start
	interrupts := make(chan os.Signal, 1)
	tests := 0
	e.Start = func(p manager.Process) error {
		switch {
		case p.Program == "/usr/bin/ssh-keygen":
			return keygen(p)
		case slices.Contains(p.Args, "-G"):
			t.Fatalf("alias queried after an interrupted connection test: %q", p.Args)
		}
		tests++
		interrupts <- os.Interrupt
		return sshExit(1)
	}
	watches, restores := 0, 0
	e.NotifyInterrupt = func() (<-chan os.Signal, func()) {
		watches++
		return interrupts, func() { restores++ }
	}
	e.Stdin = strings.NewReader("yes\n")
	var out, diag bytes.Buffer
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", "build.example", "--user", "alice"}, &out, &diag, e)
	if status != 1 || tests != 1 {
		t.Fatalf("status=%d tests=%d diagnostics=%s", status, tests, diag.String())
	}
	if watches != 2 || restores != 2 {
		t.Fatalf("interrupt watches=%d restores=%d, want one each around the question and the test", watches, restores)
	}
	keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
	if len(keys) != 2 {
		t.Fatalf("dedicated key not retained: %q", keys)
	}
	want := "SSH connection test cancelled; no host entry written, SSH configuration unchanged, dedicated key retained at " + strings.TrimSuffix(keys[0], ".pub") + "\n"
	if !strings.HasSuffix(diag.String(), want) {
		t.Fatalf("diagnostics do not end with %q: %s", want, diag.String())
	}
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("config written after an interrupted connection test: %v", err)
	}
}

// TestSSHIntegrationReportsSSHThatCannotStart proves a connection test whose ssh cannot be started, an error without an
// exit status, says the test could not run ssh, queries no alias, writes no SSH configuration, keeps the dedicated key,
// and exits non-zero, through RunWithEffects with a fake Start that fails the connection test before ssh runs.
func TestSSHIntegrationReportsSSHThatCannotStart(t *testing.T) {
	home := t.TempDir()
	tests := 0
	status, _, diag, _ := runSSHFlow(t, home, "build.example", "alice", "yes\n", func(p manager.Process) error {
		if slices.Contains(p.Args, "-G") {
			t.Fatalf("alias queried after ssh could not start: %q", p.Args)
		}
		tests++
		return &os.PathError{Op: "fork/exec", Path: p.Program, Err: os.ErrNotExist}
	})
	if status != 1 || tests != 1 {
		t.Fatalf("status=%d tests=%d diagnostics=%s", status, tests, diag)
	}
	keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
	if len(keys) != 2 {
		t.Fatalf("dedicated key not retained: %q", keys)
	}
	want := "SSH connection test could not run ssh; no host entry written, SSH configuration unchanged, dedicated key retained at " + strings.TrimSuffix(keys[0], ".pub") + "\n"
	if !strings.HasSuffix(diag, want) {
		t.Fatalf("diagnostics do not end with %q: %s", want, diag)
	}
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Fatalf("config written after ssh could not start: %v", err)
	}
}

// sshExit is a recorded ssh exit status at the process seam, as exec reports one.
type sshExit int

// Error describes the recorded exit status.
func (e sshExit) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// ExitCode returns the recorded exit status; a negative value records death by a signal.
func (e sshExit) ExitCode() int { return int(e) }

// TestSSHIntegrationTestsConnectionWithPasswordOnly proves an approved connection test starts the image's ssh once as
// the `agent` user with the fixed SSH environment and only password authentication: no key, ssh-agent, host-based, or
// GSSAPI login, no configuration file, no system-wide known hosts file, an interactive confirmation of an unknown host
// key that is recorded in the `agent` user's known_hosts, and no password or key path in its arguments, environment, or
// input, through RunWithEffects and a recorded ssh start.
func TestSSHIntegrationTestsConnectionWithPasswordOnly(t *testing.T) {
	home := t.TempDir()
	var tests []manager.Process
	status, _, diag, _ := runSSHFlow(t, home, "Build.Example", "alice", "yes\n", func(p manager.Process) error {
		tests = append(tests, p)
		fmt.Fprint(p.Stderr, "alice@build.example: Permission denied (password).\r\n")
		return sshExit(255)
	})
	if status != 1 || len(tests) != 1 {
		t.Fatalf("status=%d tests=%d diagnostics=%s", status, len(tests), diag)
	}
	p := tests[0]
	want := []string{"-F", "none", "-n", "-T",
		"-o", "BatchMode=no",
		"-o", "ConnectTimeout=15",
		"-o", "StrictHostKeyChecking=ask",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts"),
		"-o", "GlobalKnownHostsFile=none",
		"-o", "UpdateHostKeys=no",
		"-o", "PreferredAuthentications=password,keyboard-interactive",
		"-o", "PasswordAuthentication=yes",
		"-o", "KbdInteractiveAuthentication=yes",
		"-o", "PubkeyAuthentication=no",
		"-o", "HostbasedAuthentication=no",
		"-o", "GSSAPIAuthentication=no",
		"-o", "IdentityAgent=none",
		"-l", "alice", "--", "build.example", "exit"}
	if p.Program != "/usr/bin/ssh" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || p.EnvironmentPolicy != manager.SSHEnvironment || len(p.Environment) != 0 || !slices.Equal(p.Args, want) {
		t.Fatalf("connection test process %+v\nwant args %q", p, want)
	}
	if strings.Contains(strings.Join(p.Args, " "), "id_ed25519_remote_") {
		t.Fatalf("dedicated key offered to the remote machine: %q", p.Args)
	}
	if p.Stdin == nil {
		t.Fatal("connection test inherits the manager's standard input")
	}
	if input, _ := io.ReadAll(p.Stdin); len(input) != 0 {
		t.Fatalf("connection test received input %q", input)
	}
}

// TestSSHIntegrationNamesEachConnectionFailure proves each recorded ssh failure, exit status 255 with its diagnostic or
// death by a signal, maps to a recognizable cause, shows ssh's own diagnostic, exits non-zero, queries no alias, keeps the dedicated key, and leaves an existing
// ~/.ssh/config byte for byte unchanged, through RunWithEffects and a scripted ssh connection test.
func TestSSHIntegrationNamesEachConnectionFailure(t *testing.T) {
	for _, c := range []struct {
		name, host, diagnostic string
		exit                   int
		says                   []string
	}{
		{"name not resolvable", "build.example", "ssh: Could not resolve hostname build.example: Name or service not known\r\n", 255, []string{"remote machine name build.example cannot be resolved"}},
		{"timed out", "build.example", "ssh: connect to host build.example port 22: Connection timed out\r\n", 255, []string{"remote machine build.example is unreachable or timed out"}},
		{"no route", "192.0.2.10", "ssh: connect to host 192.0.2.10 port 22: No route to host\r\n", 255, []string{"remote machine 192.0.2.10 is unreachable or timed out"}},
		{"refused", "build.example", "ssh: connect to host build.example port 22: Connection refused\r\n", 255, []string{"remote machine build.example is unreachable or timed out"}},
		{"host key rejected", "build.example", "Host key verification failed.\r\n", 255, []string{"remote host key rejected"}},
		{"host key changed", "build.example", "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\nHost key for build.example has changed and you have requested strict checking.\r\nHost key verification failed.\r\n", 255, []string{"remote host key rejected", "changed"}},
		{"authentication failed", "build.example", "alice@build.example: Permission denied (publickey,password).\r\n", 255, []string{"authentication as alice failed"}},
		{"no password method", "build.example", "alice@build.example: Permission denied (publickey).\r\n", 255, []string{"authentication as alice failed", "does not offer password authentication"}},
		{"interrupted", "build.example", "", -1, []string{"SSH connection test cancelled"}},
		{"other", "build.example", "ssh: unexpected failure\r\n", 255, []string{"SSH connection test failed with exit status 255"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			config := []byte("# kept\nHost other\n    User bob\n")
			if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), config, 0600); err != nil {
				t.Fatal(err)
			}
			tests := 0
			status, _, diag, _ := runSSHFlow(t, home, c.host, "alice", "y\n", func(p manager.Process) error {
				if slices.Contains(p.Args, "-G") {
					t.Fatalf("alias queried after a failed connection test: %q", p.Args)
				}
				tests++
				fmt.Fprint(p.Stderr, c.diagnostic)
				return sshExit(c.exit)
			})
			if status != 1 || tests != 1 {
				t.Fatalf("status=%d tests=%d diagnostics=%s", status, tests, diag)
			}
			for _, says := range append(c.says, "no host entry written", "dedicated key retained at ") {
				if !strings.Contains(diag, says) {
					t.Fatalf("diagnostics lack %q: %s", says, diag)
				}
			}
			if !strings.Contains(diag, c.diagnostic) {
				t.Fatalf("ssh diagnostic not shown: %s", diag)
			}
			if got, err := os.ReadFile(filepath.Join(home, ".ssh", "config")); err != nil || !bytes.Equal(got, config) {
				t.Fatalf("SSH configuration changed: %q %v", got, err)
			}
			keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(keys) != 2 {
				t.Fatalf("dedicated key not retained: %q", keys)
			}
		})
	}
}

// TestSSHIntegrationWritesAliasOnlyAfterSuccessfulTest proves a successful connection test is followed by the alias host
// entry unit: a machine name becomes its own alias and an address takes the alias the user answers next, the entry
// resolves through the local ssh -G to the target, remote username, and dedicated key, the command exits zero, and its
// report does not claim key-based login was verified, through RunWithEffects with a scripted connection test and the
// local OpenSSH client answering the alias unit's ssh -G queries without any connection.
func TestSSHIntegrationWritesAliasOnlyAfterSuccessfulTest(t *testing.T) {
	for _, c := range []struct{ host, answers, alias, hostName string }{
		{"Build.Example", "yes\n", "build.example", "build.example"},
		{"192.0.2.10", "yes\nbuild01\n", "build01", "192.0.2.10"},
	} {
		t.Run(c.host, func(t *testing.T) {
			home := t.TempDir()
			tested, queriedEarly := false, false
			status, out, diag, _ := runSSHFlow(t, home, c.host, "alice", c.answers, func(p manager.Process) error {
				if !slices.Contains(p.Args, "-G") {
					tested = true
					return nil
				}
				queriedEarly = queriedEarly || !tested
				return localSSHQuery(home, p)
			})
			if status != 0 || !tested || queriedEarly {
				t.Fatalf("status=%d tested=%v queriedEarly=%v diagnostics=%s", status, tested, queriedEarly, diag)
			}
			keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(keys) != 2 {
				t.Fatalf("keys=%q", keys)
			}
			var resolved bytes.Buffer
			query := exec.Command("/usr/bin/ssh", "-G", "-F", filepath.Join(home, ".ssh", "config"), c.alias)
			query.Stdout, query.Env = &resolved, []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C"}
			if err := query.Run(); err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{"hostname " + c.hostName, "user alice", "identityfile " + keys[0], "identitiesonly yes"} {
				if !strings.Contains(resolved.String(), line+"\n") {
					t.Fatalf("ssh -G %s lacks %q:\n%s", c.alias, line, &resolved)
				}
			}
			if !strings.HasPrefix(out, "ssh-ed25519 ") || strings.Count(out, "\n") != 1 {
				t.Fatalf("standard output beyond the public key: %q", out)
			}
			if !strings.Contains(diag, "SSH alias "+c.alias+" configured") || !strings.Contains(diag, "key-based login is not verified") || strings.Contains(strings.ToLower(diag), "key-based login verified") {
				t.Fatalf("success report: %s", diag)
			}
		})
	}
}

// TestSSHIntegrationCountsRemoteExitStatusAsLogin proves an exit status other than ssh's own 255, as a restricted remote
// shell or a forced command returns after the password was accepted, counts as a successful login even when the remote
// output reads like an ssh failure: the flow runs the alias host entry unit and exits zero, through RunWithEffects with a
// scripted connection test and the local OpenSSH client answering the alias unit's ssh -G queries.
func TestSSHIntegrationCountsRemoteExitStatusAsLogin(t *testing.T) {
	for _, exit := range []int{1, 127} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			home := t.TempDir()
			tested := false
			status, _, diag, _ := runSSHFlow(t, home, "build.example", "alice", "yes\n", func(p manager.Process) error {
				if slices.Contains(p.Args, "-G") {
					return localSSHQuery(home, p)
				}
				tested = true
				fmt.Fprint(p.Stderr, "'exit' is not recognized: Permission denied; connection refused\r\n")
				return sshExit(exit)
			})
			if status != 0 || !tested || !strings.Contains(diag, "SSH alias build.example configured") {
				t.Fatalf("status=%d tested=%v diagnostics=%s", status, tested, diag)
			}
			if config, err := os.ReadFile(filepath.Join(home, ".ssh", "config")); err != nil || !strings.Contains(string(config), "Host build.example") {
				t.Fatalf("alias host entry not written: %q %v", config, err)
			}
		})
	}
}

// localSSHQuery answers one ssh -G query of the alias unit with the local OpenSSH client in the fixed SSH environment for
// home; ssh -G only prints the resolved configuration and opens no connection.
func localSSHQuery(home string, p manager.Process) error {
	query := exec.Command(p.Program, p.Args...)
	query.Stdin, query.Stdout, query.Stderr = p.Stdin, p.Stdout, p.Stderr
	query.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
	return query.Run()
}
