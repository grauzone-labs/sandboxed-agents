package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const readinessHostKey = "[127.0.0.1]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"

// TestReadinessRetriesOnlyPendingProbes proves both checks run immediately and only pending checks retry every 250ms through the injected readiness process and clock seam.
func TestReadinessRetriesOnlyPendingProbes(t *testing.T) {
	for _, slow := range []string{"podman", "ssh"} {
		t.Run(slow, func(t *testing.T) {
			now := time.Now()
			start := now
			counts := map[string]int{}
			var waits []time.Duration
			r := readinessRuntime{now: func() time.Time { return now }, wait: func(_ context.Context, d time.Duration) error { waits = append(waits, d); now = now.Add(d); return nil }}
			r.run = func(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
				name := filepath.Base(cmd.Path)
				counts[name]++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 3*time.Second || cmd.Cancel == nil {
					t.Fatal("probe cancellation missing")
				}
				if name == slow && counts[name] == 1 {
					return nil, fmt.Errorf("not ready")
				}
				if name == "podman" {
					return []byte("sandboxed-agents-manager development\n"), nil
				}
				if err := os.WriteFile(filepath.Join(cmd.Dir, "known_hosts"), []byte(readinessHostKey), 0600); err != nil {
					t.Fatal(err)
				}
				return []byte("debug1: SSH2_MSG_NEWKEYS received\n"), fmt.Errorf("authentication refused")
			}
			p := targetForHost((Host{}).defaults())
			err := waitReadiness(context.Background(), p, "sandboxed-agents.default.demo", 2222, r)
			if err != nil {
				t.Fatal(err)
			}
			if counts[slow] != 2 || counts[map[string]string{"podman": "ssh", "ssh": "podman"}[slow]] != 1 || len(waits) != 1 || waits[0] != 250*time.Millisecond || now.Sub(start) != 250*time.Millisecond {
				t.Fatalf("counts=%v waits=%v", counts, waits)
			}
		})
	}
}

// TestReadinessDeadlineIncludesProbeTime proves slow failures consume the 60s budget and the final probe is cut to the remaining time through injected process and clock functions.
func TestReadinessDeadlineIncludesProbeTime(t *testing.T) {
	now := time.Now()
	start := now
	count := 0
	finalBudget := time.Duration(0)
	r := readinessRuntime{now: func() time.Time { return now }, wait: func(_ context.Context, d time.Duration) error {
		if d > 250*time.Millisecond {
			t.Fatalf("wait %v", d)
		}
		now = now.Add(d)
		return nil
	}}
	r.run = func(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("no deadline")
		}
		remaining := 60*time.Second - now.Sub(start)
		want := min(3*time.Second, remaining)
		budget := time.Until(deadline)
		if budget > want || budget < want-100*time.Millisecond {
			t.Fatalf("probe budget=%v want=%v", budget, want)
		}
		now = now.Add(want)
		count++
		finalBudget = want
		return nil, fmt.Errorf("probe failed")
	}
	err := waitReadiness(context.Background(), targetForHost((Host{}).defaults()), "sandboxed-agents.default.demo", 2222, r)
	if err == nil || !strings.Contains(err.Error(), "60s deadline exceeded") || now.Sub(start) != 60*time.Second || count != 20 || finalBudget != 750*time.Millisecond {
		t.Fatalf("error=%v elapsed=%v count=%d final=%v", err, now.Sub(start), count, finalBudget)
	}
}

// TestReadinessCancellationStopsProbes proves cancellation reports its cause without another process or wait through the injected readiness seam.
func TestReadinessCancellationStopsProbes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := readinessRuntime{now: time.Now, run: func(context.Context, *exec.Cmd) ([]byte, error) { t.Fatal("probe after cancellation"); return nil, nil }}
	err := waitReadiness(ctx, targetForHost((Host{}).defaults()), "sandboxed-agents.default.demo", 2222, r)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error=%v", err)
	}
}

// TestSSHReadinessRequiresVerifiedEd25519Exchange proves a host key alone, debug lookalikes, wrong ports and malformed keys cannot pass through the SSH process boundary.
func TestSSHReadinessRequiresVerifiedEd25519Exchange(t *testing.T) {
	for _, tc := range []struct {
		name, log, key string
		ok             bool
	}{
		{"valid", "debug1: SSH2_MSG_NEWKEYS received\n", readinessHostKey, true},
		{"key only", "", readinessHostKey, false},
		{"banner lookalike", "banner: debug1: SSH2_MSG_NEWKEYS received\n", readinessHostKey, false},
		{"wrong port", "debug1: SSH2_MSG_NEWKEYS received\n", strings.Replace(readinessHostKey, "2222", "2223", 1), false},
		{"invalid key", "debug1: SSH2_MSG_NEWKEYS received\n", "[127.0.0.1]:2222 ssh-ed25519 broken\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var directory string
			err := probeSSH(context.Background(), 2222, func(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
				directory = cmd.Dir
				args := strings.Join(cmd.Args, " ")
				for _, want := range []string{"-v -F none", "BatchMode=yes", "StrictHostKeyChecking=accept-new", "IdentityAgent=none", "UserKnownHostsFile=known_hosts", "PubkeyAuthentication=no", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "HostbasedAuthentication=no", "GSSAPIAuthentication=no"} {
					if !strings.Contains(args, want) {
						t.Errorf("missing option %s", want)
					}
				}
				if err := os.WriteFile(filepath.Join(directory, "known_hosts"), []byte(tc.key), 0600); err != nil {
					t.Fatal(err)
				}
				return []byte(tc.log), fmt.Errorf("authentication failed")
			})
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v wantOK=%v", err, tc.ok)
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("temporary directory remains: %v", err)
			}
		})
	}
}
