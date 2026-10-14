package manager

import (
	"fmt"
	"io"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

func sshAuthorize(text string, stderr io.Writer, effects Effects) int {
	if effects.Identity == nil || effects.Identity() != rootIdentity {
		fmt.Fprintln(stderr, "SSH administration requires container root")
		return 1
	}
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
	if effects.Identity == nil || effects.Identity() != rootIdentity {
		fmt.Fprintln(stderr, "SSH administration requires container root")
		return 1
	}
	if effects.ReadFile == nil {
		return exitStatus(fmt.Errorf("SSH server storage is unavailable"), stderr)
	}
	data, err := effects.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
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
