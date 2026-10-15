package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/sshkey"
)

type readinessRuntime struct {
	now  func() time.Time
	wait func(context.Context, time.Duration) error
	run  func(context.Context, *exec.Cmd) ([]byte, error)
}

func readySandbox(p podmanTarget, container string, port uint16) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return waitReadiness(ctx, p, container, port, readinessRuntime{time.Now, waitContext, func(_ context.Context, cmd *exec.Cmd) ([]byte, error) { return cmd.Output() }})
}

func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitReadiness(ctx context.Context, p podmanTarget, container string, port uint16, r readinessRuntime) error {
	deadline := r.now().Add(60 * time.Second)
	ready := [2]bool{}
	failures := [2]error{}
	for {
		for i := range ready {
			if ready[i] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("readiness wait failed: %w", err)
			}
			remaining := deadline.Sub(r.now())
			if remaining <= 0 {
				return readinessTimeout(failures)
			}
			// Reserve pipe-drain time inside the probe budget as well as the total wait.
			timeout := min(3*time.Second, remaining)
			drain := min(25*time.Millisecond, timeout/10)
			probeCtx, cancel := context.WithTimeout(ctx, timeout-drain)
			if i == 0 {
				args, _ := ManagerQueryArgs(container, managerPath, "version")
				cmd := exec.CommandContext(probeCtx, "podman", p.arguments(args)...)
				cmd.Env = p.environment()
				cmd.WaitDelay = drain
				answer, err := r.run(probeCtx, cmd)
				if err == nil && !strings.HasPrefix(strings.TrimSpace(string(answer)), "sandboxed-agents-manager ") {
					err = fmt.Errorf("manager did not answer its version query")
				}
				failures[i] = err
			} else {
				failures[i] = probeSSH(probeCtx, port, r.run)
			}
			if probeCtx.Err() != nil {
				failures[i] = probeCtx.Err()
			}
			cancel()
			ready[i] = failures[i] == nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("readiness wait failed: %w", err)
		}
		if !r.now().Before(deadline) {
			return readinessTimeout(failures)
		}
		if ready[0] && ready[1] {
			return nil
		}
		if err := r.wait(ctx, min(250*time.Millisecond, deadline.Sub(r.now()))); err != nil {
			return fmt.Errorf("readiness wait failed: %w", err)
		}
	}
}

func readinessTimeout(failures [2]error) error {
	return fmt.Errorf("readiness wait failed: 60s deadline exceeded (manager: %v; SSH: %v)", failures[0], failures[1])
}

// probeSSH requires the verified exchange and the matching Ed25519 known_hosts entry.
func probeSSH(ctx context.Context, port uint16, run func(context.Context, *exec.Cmd) ([]byte, error)) error {
	dir, err := os.MkdirTemp("", "sandboxed-agents-readiness-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "ssh", "-v", "-F", "none",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "IdentityAgent=none",
		"-o", "UserKnownHostsFile=known_hosts", "-o", "GlobalKnownHostsFile=none", "-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "HostbasedAuthentication=no", "-o", "GSSAPIAuthentication=no", "-o", "HashKnownHosts=no",
		"-p", strconv.Itoa(int(port)), "agent@127.0.0.1")
	cmd.Dir = dir
	cmd.WaitDelay = 25 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		cmd.WaitDelay = min(cmd.WaitDelay, max(time.Nanosecond, time.Until(deadline)/10))
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, _ := run(ctx, cmd)
	output = append(stderr.Bytes(), output...)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slices.Contains(strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n"), "debug1: SSH2_MSG_NEWKEYS received") {
		return fmt.Errorf("SSH key exchange did not complete")
	}
	data, err := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if err != nil {
		return err
	}
	host := fmt.Sprintf("[127.0.0.1]:%d", port)
	if port == 22 {
		host = "127.0.0.1"
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == host {
			if _, err := sshkey.Ed25519(strings.Join(fields[1:3], " ")); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("SSH exchange has no Ed25519 host key for %s", host)
}
