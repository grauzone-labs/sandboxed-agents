package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMachineQueriesRefuseAChangedHostKeyWithinOnePreflight proves the machine queries of one
// preflight share a private known_hosts file that records the machine's Ed25519 key on first
// contact and refuses a different key on a later query, using the host's real ssh against the
// test exchange peer, which presents a fresh host key on every connection, as ADR-0008 requires.
func TestMachineQueriesRefuseAChangedHostKeyWithinOnePreflight(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("host ssh is unavailable")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	served := make(chan error, 2)
	go func() {
		for range 2 {
			c, err := listener.Accept()
			if err != nil {
				served <- err
				return
			}
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			served <- serveTestExchange(c, "valid")
			c.Close()
		}
	}()
	identity := filepath.Join(t.TempDir(), "machine identity")
	if err := os.WriteFile(identity, nil, 0600); err != nil {
		t.Fatal(err)
	}
	defer SetMachineQueryTimeout(5 * time.Second)()
	queries, err := newMachineQueries("chosen", machineSSHConfig{IdentityPath: identity, Port: listener.Addr().(*net.TCPAddr).Port, RemoteUsername: "core"})
	if err != nil {
		t.Fatal(err)
	}
	defer queries.close()
	// The first query records the peer's key: the exchange completes and the peer closes the
	// connection before any command runs, so the client fails after the key was accepted.
	_, first := queries.run("id", "-u")
	if first == nil || errors.Is(first, context.DeadlineExceeded) {
		t.Fatalf("first query against the exchange peer: %v", first)
	}
	if err := <-served; err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	recorded, err := os.ReadFile(filepath.Join(queries.dir, "known_hosts"))
	if err != nil || !strings.Contains(string(recorded), "ssh-ed25519") {
		t.Fatalf("known_hosts after the first query: %v %q", err, recorded)
	}
	// The second query meets a different key for the same machine endpoint and must fail closed.
	_, second := queries.run("id", "-u")
	<-served
	if second == nil || !strings.Contains(second.Error(), "Host key verification failed") {
		t.Fatalf("second query with a changed host key: %v", second)
	}
	if after, err := os.ReadFile(filepath.Join(queries.dir, "known_hosts")); err != nil || string(after) != string(recorded) {
		t.Fatalf("known_hosts changed by the refused query: %v %q", err, after)
	}
}
