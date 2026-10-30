package manager

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// errSSHTestCancelled reports end of input at the approval question; the flow makes no connection.
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
