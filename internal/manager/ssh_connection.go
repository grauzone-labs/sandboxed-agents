package manager

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sshConnectTimeout bounds, in seconds, how long the connection test waits for the remote machine to answer.
const sshConnectTimeout = "15"

// sshFailureStatus is the exit status ssh reserves for its own failures, such as an unreachable machine, a rejected host
// key, or failed authentication; the remote command's status passes through as any other value.
const sshFailureStatus = 255

// errSSHTestCancelled reports end of input or Ctrl+C at the approval question, or an interrupted connection test.
var errSSHTestCancelled = errors.New("SSH connection test cancelled")

// approveSSHTest asks on stderr whether to test the connection to user@target with password authentication and reads
// answer lines from stdin without consuming later input. It returns true for y or yes and false for n, no, or an empty
// answer, ignoring case and surrounding space, asks again after any other answer, and returns errSSHTestCancelled at end
// of input. It starts no process, so no connection precedes the user's approval.
func approveSSHTest(stdin io.Reader, user, target string, stderr io.Writer) (bool, error) {
	for {
		fmt.Fprintf(stderr, "Test the connection to %s@%s with password authentication now? ssh asks for the password. [y/N]: ", user, target)
		line, err := readSSHAnswer(stdin)
		if err != nil {
			return false, errSSHTestCancelled
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		}
		fmt.Fprintln(stderr, "answer yes or no")
	}
}

// awaitSSHApproval asks for approval of the connection test as approveSSHTest does while Ctrl+C is caught, and returns
// errSSHTestCancelled as soon as an interrupt arrives, so an interrupted question ends the flow with its message instead
// of ending the manager silently. The unanswered read is left behind; the flow ends without reading again.
func awaitSSHApproval(stdin io.Reader, user, target string, stderr io.Writer, effects Effects) (bool, error) {
	interrupts, stop := watchInterrupts(effects)
	defer stop()
	type answer struct {
		approved bool
		err      error
	}
	answers := make(chan answer, 1)
	go func() {
		approved, err := approveSSHTest(stdin, user, target, stderr)
		answers <- answer{approved, err}
	}()
	select {
	case a := <-answers:
		return a.approved, a.err
	case <-interrupts:
		return false, errSSHTestCancelled
	}
}

// testSSHConnection logs in to target as user once with the image's ssh, run as agent in the fixed SSH environment, and
// returns nil when the login succeeds. The remote command is exit, which ends a POSIX shell, cmd, and PowerShell alike;
// its exit status is not the test, so any status other than ssh's own 255 counts as a successful login, even from a
// restricted remote shell or a forced command. It reads no configuration file and allows only password and
// keyboard-interactive authentication, so no key, ssh-agent, host-based, or GSSAPI login is attempted and the dedicated
// key is neither offered nor installed. ssh itself reads the password from the terminal without echo; the password never
// passes through the manager, ssh's arguments, its environment, or its standard input, which is empty. ssh shows an
// unknown host key with its fingerprint and asks before it sends the password; a confirmed key is recorded in the
// agent's ~/.ssh/known_hosts, a refused or changed one ends the test. ssh's diagnostics reach stderr and are kept,
// bounded, to name the failure as sshTestFailure does.
func testSSHConnection(home, target, user string, stderr io.Writer, effects Effects) error {
	knownHosts, err := sshConfigValue(filepath.Join(home, ".ssh", "known_hosts"), true)
	if err != nil {
		return fmt.Errorf("SSH connection test failed: the known_hosts path cannot be passed to ssh safely")
	}
	args := []string{"-F", "none", "-n", "-T",
		"-o", "BatchMode=no",
		"-o", "ConnectTimeout=" + sshConnectTimeout,
		"-o", "StrictHostKeyChecking=ask",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "UpdateHostKeys=no",
		"-o", "PreferredAuthentications=password,keyboard-interactive",
		"-o", "PasswordAuthentication=yes",
		"-o", "KbdInteractiveAuthentication=yes",
		"-o", "PubkeyAuthentication=no",
		"-o", "HostbasedAuthentication=no",
		"-o", "GSSAPIAuthentication=no",
		"-o", "IdentityAgent=none",
		"-l", user, "--", target, "exit"}
	diagnostics := sshOutputBuffer{limit: sshDiagnosticLimit}
	interrupts, stop := watchInterrupts(effects)
	defer stop()
	err = effects.Start(Process{Program: sshProgram, Args: args, Identity: agentIdentity, EnvironmentPolicy: SSHEnvironment, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.MultiWriter(&diagnostics, stderr)})
	if err == nil {
		return nil
	}
	return sshTestFailure(target, user, err, string(diagnostics.data), interrupts)
}

// sshTestFailure names the outcome of a connection test that did not exit zero: cancelled when Ctrl+C arrived or ssh died
// of a signal, and nil for any exit status other than 255, because ssh reports its own failures with 255 and any other
// status comes from the remote command after the login succeeded. Only for status 255 do ssh's diagnostics name the
// cause: a rejected or changed remote host key, failed authentication, a remote machine name that cannot be resolved,
// or a remote machine that is unreachable or timed out; otherwise the exit status.
func sshTestFailure(target, user string, err error, diagnostics string, interrupts <-chan os.Signal) error {
	interrupted := false
	if interrupts != nil {
		select {
		case <-interrupts:
			interrupted = true
		default:
		}
	}
	var exit interface{ ExitCode() int }
	hasExit := errors.As(err, &exit)
	switch {
	case interrupted || (hasExit && exit.ExitCode() < 0):
		return errSSHTestCancelled
	case !hasExit:
		return fmt.Errorf("SSH connection test could not run ssh")
	case exit.ExitCode() != sshFailureStatus:
		return nil
	}
	text := strings.ToLower(diagnostics)
	contains := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if strings.Contains(text, phrase) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("remote host identification has changed", "has changed and you have requested strict checking"):
		return fmt.Errorf("SSH connection test failed: remote host key rejected; the host key of %s changed since it was trusted in ~/.ssh/known_hosts and is refused", target)
	case contains("host key verification failed"):
		return fmt.Errorf("SSH connection test failed: remote host key rejected; the host key of %s was not confirmed", target)
	case contains("permission denied", "too many authentication failures", "no more authentication methods"):
		if !sshOffersPassword(text) {
			return fmt.Errorf("SSH connection test failed: authentication as %s failed; the remote machine does not offer password authentication", user)
		}
		return fmt.Errorf("SSH connection test failed: authentication as %s failed", user)
	case contains("could not resolve hostname", "name or service not known", "temporary failure in name resolution", "no address associated with hostname"):
		return fmt.Errorf("SSH connection test failed: remote machine name %s cannot be resolved", target)
	case contains("timed out", "no route to host", "network is unreachable", "host is unreachable", "host is down", "connection refused", "connection reset", "connection closed"):
		return fmt.Errorf("SSH connection test failed: remote machine %s is unreachable or timed out", target)
	}
	return fmt.Errorf("SSH connection test failed with exit status %d", exit.ExitCode())
}

// sshOffersPassword reports whether the methods ssh lists after "Permission denied" in the lowercased diagnostics include
// password or keyboard-interactive authentication; without such a list it assumes they do.
func sshOffersPassword(text string) bool {
	_, rest, found := strings.Cut(text, "permission denied (")
	methods, _, closed := strings.Cut(rest, ")")
	if !found || !closed {
		return true
	}
	for _, method := range strings.Split(methods, ",") {
		if method == "password" || method == "keyboard-interactive" {
			return true
		}
	}
	return false
}
