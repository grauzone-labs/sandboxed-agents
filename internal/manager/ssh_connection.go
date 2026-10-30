package manager

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The connection test bounds what it keeps of ssh's diagnostics to name a cause; ssh's own messages reach the user unbounded.
const (
	// sshConnectDiagnosticLimit bounds the diagnostics the connection test keeps to recognize the cause of a failure.
	sshConnectDiagnosticLimit = 64 << 10
	// sshConnectTimeout is the ConnectTimeout, in seconds, after which an unanswered connection counts as timed out.
	sshConnectTimeout = "15"
	// sshFailureStatus is the exit status ssh reports for its own errors; any other status is the remote command's.
	sshFailureStatus = 255
)

// errSSHApprovalCancelled reports a connection test approval that ended without an answer, by end of input or Ctrl+C.
var errSSHApprovalCancelled = errors.New("SSH connection test cancelled")

// connectSSH asks the user to approve the password connection test of user at target, runs it, and on success adds the alias host
// entry through configureSSHAlias with the dedicated key keyName; keyPath is the key's location, which every other outcome names as
// retained. It returns the exit status and reports each outcome on stderr. Declining, cancelling, or any failure makes no host entry
// and leaves ~/.ssh/config unchanged; a host key the user confirmed at ssh's prompt stays in known_hosts (ADR-0011).
func connectSSH(target, user, keyName, keyPath, home string, stderr io.Writer, effects Effects) int {
	retained := fmt.Sprintf("no SSH host entry written; dedicated SSH key retained at %s", keyPath)
	interrupts, restore := watchInterrupts(effects)
	defer restore()
	stdin := effects.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	approved, err := askSSHApproval(stdin, interrupts, target, user, stderr)
	if err != nil || !approved {
		outcome := "SSH connection test declined"
		if err != nil {
			outcome = err.Error()
		}
		fmt.Fprintf(stderr, "%s; no connection made; %s\n", outcome, retained)
		return 1
	}
	if err := testSSHConnection(target, user, home, interrupts, stderr, effects); err != nil {
		fmt.Fprintf(stderr, "%s; %s\n", err, retained)
		return 1
	}
	restore()
	fmt.Fprintf(stderr, "SSH connection test to %s@%s succeeded with password authentication; this does not prove that the dedicated key is authorized on the remote machine\n", user, target)
	alias, err := configureSSHAlias(target, user, keyName, stderr, effects)
	if err != nil {
		fmt.Fprintf(stderr, "%s; %s\n", err, retained)
		return 1
	}
	fmt.Fprintf(stderr, "SSH alias %s added to ~/.ssh/config; connect with ssh %s once the public key is authorized on the remote machine\n", alias, alias)
	return 0
}

// askSSHApproval asks on stderr whether to test the connection and reads one answer line from stdin; yes or y in any letter case
// approves and every other answer declines. End of input, or Ctrl+C on interrupts while the question waits, cancels with
// errSSHApprovalCancelled, without waiting for an answer that never comes.
func askSSHApproval(stdin io.Reader, interrupts <-chan os.Signal, target, user string, stderr io.Writer) (bool, error) {
	fmt.Fprintf(stderr, "Test the SSH connection to %s@%s with password authentication now? ssh asks for the password; nothing connects before you answer yes. [yes/no]: ", user, target)
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		line, err := readSSHAnswer(stdin)
		answers <- answer{line, err}
	}()
	select {
	case <-interrupts:
		fmt.Fprintln(stderr)
		return false, errSSHApprovalCancelled
	case got := <-answers:
		if got.err != nil {
			fmt.Fprintln(stderr)
			return false, errSSHApprovalCancelled
		}
		switch strings.ToLower(strings.TrimSpace(got.line)) {
		case "yes", "y":
			return true, nil
		}
		return false, nil
	}
}

// testSSHConnection runs the trusted ssh client once as agent with the fixed SSH environment against user at target, with password
// authentication as the only method, so ssh offers no key and tries no key login. -F /dev/null keeps every SSH configuration file
// out of the test. StrictHostKeyChecking=ask makes ssh show an unknown host key's fingerprint and wait for the user's confirmation
// before it sends the password, store only a confirmed key in the agent's known_hosts, and refuse a changed key. ssh reads the
// password from the terminal without echo itself; the manager never sees it, and ssh's standard input is empty. The remote command
// exit 0 ends the session after authentication. ssh's diagnostics reach stderr as ssh writes them; a failure returns an error that
// names its cause as sshFailureCause recognizes it, an ssh that could not start, or the cancellation when Ctrl+C arrived.
func testSSHConnection(target, user, home string, interrupts <-chan os.Signal, stderr io.Writer, effects Effects) error {
	knownHosts, err := sshConfigValue(filepath.Join(home, ".ssh", "known_hosts"), true)
	if err != nil {
		return fmt.Errorf("SSH known_hosts path cannot be passed to ssh safely; no connection made")
	}
	args := []string{"-n", "-T", "-F", "/dev/null"}
	for _, option := range []string{
		"BatchMode=no",
		"ConnectTimeout=" + sshConnectTimeout,
		"ConnectionAttempts=1",
		"PreferredAuthentications=password",
		"PasswordAuthentication=yes",
		"PubkeyAuthentication=no",
		"KbdInteractiveAuthentication=no",
		"HostbasedAuthentication=no",
		"GSSAPIAuthentication=no",
		"IdentityAgent=none",
		"StrictHostKeyChecking=ask",
		"UserKnownHostsFile=" + knownHosts,
		"GlobalKnownHostsFile=/dev/null",
		"UpdateHostKeys=no",
		"CheckHostIP=no",
		"VerifyHostKeyDNS=no",
		"HashKnownHosts=no",
		"ClearAllForwardings=yes",
	} {
		args = append(args, "-o", option)
	}
	args = append(args, "-l", user, "--", target, "exit 0")
	diagnostic := sshOutputBuffer{limit: sshConnectDiagnosticLimit}
	err = effects.Start(Process{Program: sshProgram, Args: args, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment,
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.MultiWriter(stderr, &diagnostic)})
	var exit interface{ ExitCode() int }
	switch {
	case interruptArrived(interrupts):
		return errors.New("SSH connection test cancelled")
	case err == nil:
		return nil
	case !errors.As(err, &exit):
		return errors.New("SSH connection test failed: ssh could not start; no connection made")
	case exit.ExitCode() >= 0 && exit.ExitCode() != sshFailureStatus:
		// The remote command ran, so the machine accepted the password; its own status is not the test's.
		return nil
	}
	return sshFailureCause(string(diagnostic.data), target, user)
}

// sshFailureCause names the cause of a failed connection test from OpenSSH's diagnostics: name not resolvable, unreachable or timed
// out, host key rejected, distinguishing a changed key, and authentication failed; other failures are reported as such.
func sshFailureCause(diagnostics, target, user string) error {
	text := strings.ToLower(diagnostics)
	has := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if strings.Contains(text, phrase) {
				return true
			}
		}
		return false
	}
	switch {
	case has("remote host identification has changed", "host key for "+strings.ToLower(target)+" has changed"):
		return fmt.Errorf("SSH connection test failed: host key rejected: the host key of %s differs from the one trusted in ~/.ssh/known_hosts; changed host keys are refused", target)
	case has("host key verification failed"):
		return fmt.Errorf("SSH connection test failed: host key rejected: the host key of %s was not confirmed", target)
	case has("could not resolve hostname"):
		return fmt.Errorf("SSH connection test failed: name not resolvable: %s cannot be resolved", target)
	case has("connection timed out", "operation timed out", "connection refused", "no route to host", "network is unreachable", "host is unreachable"):
		return fmt.Errorf("SSH connection test failed: unreachable or timed out: %s did not accept a connection on port 22", target)
	case has("permission denied", "too many authentication failures", "no more authentication methods"):
		return fmt.Errorf("SSH connection test failed: authentication failed: %s did not accept the password for %s", target, user)
	}
	return fmt.Errorf("SSH connection test failed: ssh reported an error, shown above")
}
