package manager

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

func sshAdministration(args []string, stdout, stderr io.Writer, effects Effects) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	var run func() int
	switch args[0] {
	case "ssh-authorize":
		if len(args) != 2 {
			return 0, false
		}
		run = func() int { return sshAuthorize(args[1], stderr, effects) }
	case "ssh-host-key":
		if len(args) != 1 {
			return 0, false
		}
		run = func() int { return sshHostKey(stdout, stderr, effects) }
	case "ssh-deauthorize":
		if len(args) != 1 {
			return 0, false
		}
		run = func() int { return sshDeauthorize(stderr, effects) }
	default:
		return 0, false
	}
	if effects.Identity == nil || effects.Identity() != rootIdentity {
		fmt.Fprintln(stderr, "SSH administration requires container root")
		return 1, true
	}
	return run(), true
}

func sshDeauthorize(stderr io.Writer, effects Effects) int {
	if effects.RemoveAuthorizedKey == nil {
		return exitStatus(fmt.Errorf("SSH authorization storage is unavailable"), stderr)
	}
	return exitStatus(effects.RemoveAuthorizedKey(), stderr)
}

func sshAuthorize(text string, stderr io.Writer, effects Effects) int {
	key, err := sshkey.Ed25519(text)
	if err != nil {
		return exitStatus(err, stderr)
	}
	if effects.ReplaceAuthorizedKey == nil {
		return exitStatus(fmt.Errorf("SSH authorization storage is unavailable"), stderr)
	}
	return exitStatus(effects.ReplaceAuthorizedKey([]byte(key+"\n")), stderr)
}

func sshHostKey(stdout, stderr io.Writer, effects Effects) int {
	if effects.ReadFile == nil {
		return exitStatus(fmt.Errorf("SSH server storage is unavailable"), stderr)
	}
	// Podman can accept exec before the entrypoint finishes generating keys.
	// Stay within the host's three-second administrative query deadline.
	const path = "/etc/ssh/ssh_host_ed25519_key.pub"
	deadline := time.Now().Add(2 * time.Second)
	data, err := effects.ReadFile(path)
	for (os.IsNotExist(err) || err == nil && len(data) == 0) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		data, err = effects.ReadFile(path)
	}
	if err != nil {
		return exitStatus(err, stderr)
	}
	key, err := sshkey.Ed25519(string(data))
	if err != nil {
		return exitStatus(err, stderr)
	}
	fmt.Fprintln(stdout, key)
	return 0
}
