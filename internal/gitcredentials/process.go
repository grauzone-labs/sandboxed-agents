package gitcredentials

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

// HelperDeadline bounds the entire GCM process, including time waiting for gpg-agent.
const HelperDeadline = 10 * time.Second

// KillGrace is the time allowed after termination before killing the helper's process group.
const KillGrace = time.Second

// run starts a process in the store's fixed environment and bounds its owned process group.
func (s Store) run(program string, args []string, input io.Reader, output io.Writer) error {
	cmd := exec.Command(program, args...)
	cmd.Env = s.environment()
	cmd.Dir = s.Home
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, io.Discard
	cmd.WaitDelay = KillGrace
	if err := runCommand(cmd); err != nil {
		return errors.New("Git credential operation failed (store locked, unavailable, or helper deadline exceeded)")
	}
	return nil
}

// Helper runs GCM with noninteractive GPG settings under the whole-process deadline.
func (s Store) Helper(args []string, input io.Reader, output io.Writer) error {

	if len(args) == 2 && args[0] == "--generic" {
		s.generic = true
		args = args[1:]
	}
	if len(args) != 1 || (args[0] != "get" && args[0] != "store" && args[0] != "erase") {
		return errors.New("invalid Git credential helper operation")
	}
	var result bytes.Buffer
	err := s.run("git-credential-manager", args, input, &result)
	if args[0] == "get" && (err != nil || !strings.Contains(result.String(), "password=")) {
		io.WriteString(output, "quit=true\n\n")
		return errors.New("Git credential lookup failed; store locked or no stored credential")
	}
	if err != nil {
		return err
	}
	_, err = io.Copy(output, &result)
	return err
}
