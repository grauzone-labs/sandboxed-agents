package cli

import (
	"context"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// TestReadinessWithImageSSHPolicy proves update readiness accepts the embedded server policy through real OpenSSH processes, with temporary server state and only the Podman manager response substituted.
func TestReadinessWithImageSSHPolicy(t *testing.T) {
	sshd, err := exec.LookPath("/usr/sbin/sshd")
	if err != nil {
		t.Skipf("host sshd is unavailable: %v", err)
	}
	for _, program := range []string{"ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("host %s is unavailable: %v", program, err)
		}
	}
	policy, err := fs.ReadFile(image.Context(), "base/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	state := filepath.Join(directory, "etc", "ssh")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "ssh-keygen", "-A", "-f", directory).CombinedOutput(); err != nil {
		t.Fatalf("generate temporary server keys: %v\n%s", err, output)
	}
	// Relocate only server state; retain the embedded authentication policy.
	configuration := filepath.Join(directory, "sshd_config")
	if err := os.WriteFile(configuration, []byte(strings.ReplaceAll(string(policy), "/etc/ssh/", state+"/")), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(directory, "sshd.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	server := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", configuration,
		"-p", strconv.Itoa(port), "-o", "ListenAddress=127.0.0.1", "-o", "PidFile="+filepath.Join(directory, "sshd.pid"))
	server.Stderr = log
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = server.Wait() }()
	r := readinessRuntime{now: time.Now, wait: waitContext, run: func(_ context.Context, command *exec.Cmd) ([]byte, error) {
		if command.Args[0] == "podman" {
			return []byte("sandboxed-agents-manager development\n"), nil
		}
		return command.Output()
	}}
	if err := waitReadiness(ctx, targetForHost((Host{}).defaults()), "sandboxed-agents.default.demo", uint16(port), r); err != nil {
		output, _ := os.ReadFile(log.Name())
		t.Fatalf("readiness with embedded SSH policy: %v\n%s", err, output)
	}
}
