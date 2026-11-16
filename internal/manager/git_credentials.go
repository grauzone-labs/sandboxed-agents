package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/gitcredentials"
)

// credentialStore identifies the agent's dedicated credential state and the trusted manager binary.
func credentialStore(effects Effects) (gitcredentials.Store, error) {
	if effects.HomeDirectory == nil {
		return gitcredentials.Store{}, fmt.Errorf("cannot locate Git credential store")
	}
	home, err := effects.HomeDirectory()
	if err != nil {
		return gitcredentials.Store{}, fmt.Errorf("cannot locate Git credential store")
	}
	executablePath := effects.Executable
	if executablePath == nil {
		executablePath = os.Executable
	}
	executable, err := executablePath()
	return gitcredentials.Store{Home: home, Manager: executable}, err
}

// gitCredentialProgram dispatches manager-owned, nonprompting pinentry and GCM helper programs.
func gitCredentialProgram(args []string, stdout, stderr io.Writer, effects Effects) int {
	if !verifyAgent(effects, stderr) {
		return 1
	}
	store, err := credentialStore(effects)
	if err == nil {
		if args[0] == "__git-pinentry" {
			socket, e := store.AgentSocket()
			err = e
			if err == nil {
				err = gitcredentials.Pinentry(effects.Stdin, stdout, socket)
			}
		} else if args[0] == "__git-store-state" {
			var state string
			state, err = store.State()
			if err == nil {
				fmt.Fprintln(stdout, state)
			}
		} else if args[0] == "__git-store-check" {
			err = store.Check()
		} else {
			err = store.Helper(args[1:], effects.Stdin, stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "Git credential operation failed; unlock with integrations config NAME git credentials")
		return 1
	}
	return 0
}

// setupGitCredentials accepts private stdin input only after the host's terminal guard.
func setupGitCredentials(stderr io.Writer, effects Effects) int {
	store, err := credentialStore(effects)
	if err != nil {
		fmt.Fprintln(stderr, "cannot locate Git credential store")
		return 1
	}
	var input struct {
		Passphrase   string
		Confirmation string
	}
	if effects.Stdin == nil || json.NewDecoder(io.LimitReader(effects.Stdin, 4096)).Decode(&input) != nil {
		fmt.Fprintln(stderr, "cannot read Git credential passphrase")
		return 1
	}
	passphrase, confirmation := []byte(input.Passphrase), []byte(input.Confirmation)
	defer clear(passphrase)
	defer clear(confirmation)
	if err := store.Setup(passphrase, confirmation); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
