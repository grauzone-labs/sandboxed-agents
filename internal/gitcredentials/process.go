package gitcredentials

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

// HelperDeadline bounds one complete helper invocation, including pre-check, GCM and post-check.
const HelperDeadline = 10 * time.Second

// KillGrace is the time allowed after termination before killing the helper's process group.
const KillGrace = time.Second

// errHelperDeadline distinguishes bounded termination from a normal GCM miss.
var errHelperDeadline = errors.New("credential helper deadline exceeded")

// storeOperation carries an immutable deadline explicitly across one helper's stages.
// A zero deadline preserves the existing per-operation limits for standalone Store calls.
type storeOperation struct {
	Store
	deadline time.Time
}

// run starts a process in the store's fixed environment and bounds its owned process group.
func (s Store) run(program string, args []string, input io.Reader, output io.Writer) error {
	return (storeOperation{Store: s}).run(program, args, input, output)
}

// run uses this operation's deadline for process execution and cleanup.
func (op storeOperation) run(program string, args []string, input io.Reader, output io.Writer) error {
	if err := op.runWithDiagnostics(program, args, input, output, io.Discard, nil); err != nil {
		return errors.New("Git credential operation failed (store locked, unavailable, or helper deadline exceeded)")
	}
	return nil
}

// runWithDiagnostics retains private subprocess status and diagnostics for classification without forwarding them.
func (op storeOperation) runWithDiagnostics(program string, args []string, input io.Reader, output, diagnostics io.Writer, environment []string) error {
	cmd := exec.Command(program, args...)
	cmd.Env = append(op.environment(), environment...)
	cmd.Dir = op.Home
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, diagnostics
	cmd.WaitDelay = KillGrace
	return runCommand(cmd, op.deadline)
}

// Helper runs pre-check, GCM, post-check and agent IPC under one whole-helper invocation deadline.
func (s Store) Helper(args []string, input io.Reader, output io.Writer) error {
	return (storeOperation{Store: s, deadline: time.Now().Add(HelperDeadline)}).helper(args, input, output)
}

// helper executes the credential protocol with the operation's existing shared deadline.
func (op storeOperation) helper(args []string, input io.Reader, output io.Writer) (err error) {

	var providerEnvironment []string
	if len(args) == 2 && args[0] == "--generic" {
		providerEnvironment = []string{"GCM_PROVIDER=generic"}
		args = args[1:]
	}
	if len(args) != 1 || (args[0] != "get" && args[0] != "store" && args[0] != "erase") {
		return errors.New("invalid Git credential helper operation")
	}
	defer func() {
		if err != nil && args[0] == "get" {
			io.WriteString(output, "quit=true\n\n")
		}
	}()
	if args[0] == "get" {
		if err := op.Check(); err != nil {
			return errors.New("Git credential store is locked or unavailable")
		}
	}
	var result, diagnostics bytes.Buffer
	err = op.runWithDiagnostics("git-credential-manager", args, input, &result, &diagnostics, providerEnvironment)
	if args[0] == "get" && !strings.Contains(result.String(), "password=") {
		var exit *exec.ExitError
		// GCM 3.0.2 reports a missing generic credential as this noninteractive failure.
		normalMiss := err == nil || !errors.Is(err, errHelperDeadline) && errors.As(err, &exit) && exit.ExitCode() == 255 && strings.HasSuffix(strings.TrimSpace(diagnostics.String()), "fatal: Cannot prompt because user interactivity has been disabled.")
		if normalMiss && op.Check() == nil {
			return nil
		}
		return errors.New("Git credential lookup failed; store locked, unavailable, or helper failed")
	}
	if err != nil {
		return errors.New("Git credential operation failed")
	}
	_, err = io.Copy(output, &result)
	return err
}
