package manager_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

const dedicatedPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// TestSSHAuthorizationReplacesExactlyOneKeyAsRoot proves root-only authorization validates one ed25519 key and replaces the authorization atomically through the manager command and injected filesystem seam.
func TestSSHAuthorizationReplacesExactlyOneKeyAsRoot(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	var stored []byte
	effects.ReplaceAuthorizedKey = func(data []byte) error { stored = append([]byte(nil), data...); return nil }
	var stdout, stderr bytes.Buffer
	status := manager.RunWithEffects([]string{"ssh-authorize", dedicatedPublicKey + " sandbox"}, &stdout, &stderr, effects)
	if status != 0 || string(stored) != dedicatedPublicKey+"\n" {
		t.Fatalf("status=%d stored=%q stderr=%q", status, stored, stderr.String())
	}
	for _, bad := range []string{dedicatedPublicKey + "\n" + dedicatedPublicKey, "ssh-rsa AAAA", "ssh-ed25519 AAAA", "command=\"echo hi\" " + dedicatedPublicKey} {
		stored = nil
		stderr.Reset()
		status = manager.RunWithEffects([]string{"ssh-authorize", bad}, &stdout, &stderr, effects)
		if status == 0 || stored != nil || !strings.Contains(stderr.String(), "one ed25519 public key") {
			t.Fatalf("key=%q status=%d stored=%q stderr=%q", bad, status, stored, stderr.String())
		}
	}
	effects.Identity = func() manager.Identity { return manager.Identity{UID: 1000, GID: 1000} }
	stored = nil
	stderr.Reset()
	status = manager.RunWithEffects([]string{"ssh-authorize", dedicatedPublicKey}, &stdout, &stderr, effects)
	if status == 0 || stored != nil || !strings.Contains(stderr.String(), "root") {
		t.Fatalf("agent status=%d stored=%q stderr=%q", status, stored, stderr.String())
	}
}

// TestSSHDeauthorizationRunsOnlyAsRoot proves authorization removal occurs exactly once and refuses agent identity through the manager command and injected filesystem seam.
func TestSSHDeauthorizationRunsOnlyAsRoot(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	removals := 0
	effects.RemoveAuthorizedKey = func() error { removals++; return nil }
	var stdout, stderr bytes.Buffer
	if status := manager.RunWithEffects([]string{"ssh-deauthorize"}, &stdout, &stderr, effects); status != 0 || removals != 1 {
		t.Fatalf("status=%d removals=%d stderr=%q", status, removals, stderr.String())
	}
	effects.Identity = func() manager.Identity { return manager.Identity{UID: 1000, GID: 1000} }
	if status := manager.RunWithEffects([]string{"ssh-deauthorize"}, &stdout, &stderr, effects); status == 0 || removals != 1 {
		t.Fatalf("agent status=%d removals=%d", status, removals)
	}
}

// TestSSHHostKeyReadsOnlyPublicServerKeyAsRoot proves trusted host identity retrieval validates the SSH server state's public key through the manager command and injected filesystem seam.
func TestSSHHostKeyReadsOnlyPublicServerKeyAsRoot(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	reads := 0
	effects.ReadFile = func(path string) ([]byte, error) {
		reads++
		if path != "/etc/ssh/ssh_host_ed25519_key.pub" {
			t.Fatalf("read=%s", path)
		}
		return []byte(dedicatedPublicKey + " host\n"), nil
	}
	var stdout, stderr bytes.Buffer
	status := manager.RunWithEffects([]string{"ssh-host-key"}, &stdout, &stderr, effects)
	if status != 0 || stdout.String() != dedicatedPublicKey+"\n" || reads != 1 {
		t.Fatalf("status=%d stdout=%q stderr=%q reads=%d", status, stdout.String(), stderr.String(), reads)
	}
}

// TestSSHHostKeyWaitsForStartup proves a root host-key query tolerates missing or empty public-key storage during startup through the manager command and a temporary filesystem seam.
func TestSSHHostKeyWaitsForStartup(t *testing.T) {
	for _, initial := range []string{"missing", "empty"} {
		t.Run(initial, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key.pub")
			if initial == "empty" {
				if err := os.WriteFile(path, nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
			first := true
			effects.ReadFile = func(requested string) ([]byte, error) {
				if requested != "/etc/ssh/ssh_host_ed25519_key.pub" {
					t.Fatalf("unexpected read %q", requested)
				}
				data, err := os.ReadFile(path)
				if first {
					first = false
					// Publish the key after the first read, as startup would.
					if err := os.WriteFile(path, []byte(dedicatedPublicKey+" host\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				return data, err
			}
			var stdout, stderr bytes.Buffer
			status := manager.RunWithEffects([]string{"ssh-host-key"}, &stdout, &stderr, effects)
			if status != 0 || stdout.String() != dedicatedPublicKey+"\n" || stderr.Len() != 0 {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
			}
		})
	}
}

// fakeClock installs a manual clock in effects whose Sleep advances Now by the requested duration
// without waiting, and returns a function that reports the time elapsed on that clock.
func fakeClock(effects *manager.Effects) func() time.Duration {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	current := start
	effects.Now = func() time.Time { return current }
	effects.Sleep = func(_ context.Context, d time.Duration) error { current = current.Add(d); return nil }
	return func() time.Duration { return current.Sub(start) }
}

// TestSSHHostKeyWaitsSixSeconds proves a root host-key query returns a key published four
// seconds into start-up and fails without output once the key is still missing after six
// seconds, through the manager command, the injected filesystem seam and the injected clock.
func TestSSHHostKeyWaitsSixSeconds(t *testing.T) {
	t.Run("published after four seconds", func(t *testing.T) {
		effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
		elapsed := fakeClock(&effects)
		effects.ReadFile = func(string) ([]byte, error) {
			if elapsed() < 4*time.Second {
				return nil, os.ErrNotExist
			}
			return []byte(dedicatedPublicKey + " host\n"), nil
		}
		var stdout, stderr bytes.Buffer
		status := manager.RunWithEffects([]string{"ssh-host-key"}, &stdout, &stderr, effects)
		if status != 0 || stdout.String() != dedicatedPublicKey+"\n" || stderr.Len() != 0 {
			t.Fatalf("status=%d stdout=%q stderr=%q elapsed=%s", status, stdout.String(), stderr.String(), elapsed())
		}
		if got := elapsed(); got < 4*time.Second || got >= 5*time.Second {
			t.Fatalf("key returned after %s; want about 4s", got)
		}
	})
	t.Run("never published", func(t *testing.T) {
		effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
		elapsed := fakeClock(&effects)
		effects.ReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
		var stdout, stderr bytes.Buffer
		status := manager.RunWithEffects([]string{"ssh-host-key"}, &stdout, &stderr, effects)
		if status == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
		}
		if got := elapsed(); got < 6*time.Second || got > 6*time.Second+100*time.Millisecond {
			t.Fatalf("gave up after %s; want 6s", got)
		}
	})
}

// TestSSHHostKeyFailsClosed proves unavailable or invalid host-key storage never emits a pin and nontransient errors are not retried through the manager command, the filesystem error seam and the injected clock.
func TestSSHHostKeyFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		err  error
	}{
		{name: "missing after startup", err: os.ErrNotExist},
		{name: "permission denied", err: os.ErrPermission},
		{name: "malformed key", data: []byte("ssh-ed25519 invalid")},
	} {
		t.Run(test.name, func(t *testing.T) {
			effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
			fakeClock(&effects)
			reads := 0
			effects.ReadFile = func(string) ([]byte, error) {
				reads++
				return test.data, test.err
			}
			var stdout, stderr bytes.Buffer
			status := manager.RunWithEffects([]string{"ssh-host-key"}, &stdout, &stderr, effects)
			if status == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
			}
			if test.err != os.ErrNotExist && reads != 1 {
				t.Fatalf("retried nontransient host-key failure %d times", reads)
			}
		})
	}
}
