package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

// sshAdministration dispatches recognized SSH requests after requiring container root.
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

// sshDeauthorize removes managed authorization through the injected storage operation.
func sshDeauthorize(stderr io.Writer, effects Effects) int {
	if effects.RemoveAuthorizedKey == nil {
		return exitStatus(fmt.Errorf("SSH authorization storage is unavailable"), stderr)
	}
	return exitStatus(effects.RemoveAuthorizedKey(), stderr)
}

// sshAuthorize validates one Ed25519 public key and replaces the managed authorization.
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

// sshHostKeyWait bounds how long ssh-host-key waits for the entrypoint's ssh-keygen to publish
// the public key, since podman can accept exec before the entrypoint has generated it. The live
// host-key helper gives the whole podman exec a 9 s deadline; 6 s leaves about 3 s of that for
// podman exec start-up. It also stays inside the 10 s budget of the CLI's first read-only manager
// attempt, which gives podman exec 9 s before its 1 s output drain.
const sshHostKeyWait = 6 * time.Second

// sshHostKeyClock returns the injected clock and sleep for the host-key wait, and the system clock
// and a plain sleep for whichever of the two the effects leave unset.
func sshHostKeyClock(effects Effects) (func() time.Time, func(time.Duration)) {
	now, sleep := time.Now, time.Sleep
	if effects.Now != nil {
		now = effects.Now
	}
	if effects.Sleep != nil {
		sleep = func(d time.Duration) { _ = effects.Sleep(context.Background(), d) }
	}
	return now, sleep
}

// sshHostKey polls for the server's public key while it is missing or empty, for up to
// sshHostKeyWait on the injected clock, and prints the validated Ed25519 public key. Any other read
// error, a key still absent after the wait, or an invalid key fails without output.
func sshHostKey(stdout, stderr io.Writer, effects Effects) int {
	if effects.ReadFile == nil {
		return exitStatus(fmt.Errorf("SSH server storage is unavailable"), stderr)
	}
	now, sleep := sshHostKeyClock(effects)
	const path = "/etc/ssh/ssh_host_ed25519_key.pub"
	deadline := now().Add(sshHostKeyWait)
	data, err := effects.ReadFile(path)
	for (os.IsNotExist(err) || err == nil && len(data) == 0) && now().Before(deadline) {
		sleep(25 * time.Millisecond)
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
