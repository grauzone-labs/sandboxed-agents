package gitcredentials_test

import (
	"bytes"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/gitcredentials"
)

// TestPinentryReadsOnlyProductSecret proves the nonprompting adapter answers GETPIN over Assuan using the dedicated socket at the pinentry API seam.
func TestPinentryReadsOnlyProductSecret(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- "accept failed"
			return
		}
		defer conn.Close()
		conn.Write([]byte("OK agent\n"))
		b := make([]byte, 1024)
		n, _ := conn.Read(b)
		done <- string(b[:n])
		conn.Write([]byte("D dummy%25phrase\nOK\n"))
	}()
	var output bytes.Buffer
	err = gitcredentials.Pinentry(strings.NewReader("OPTION ttyname=/dev/null\nSETDESC ignored\nGETPIN\nBYE\n"), &output, socket)
	if err != nil || !strings.Contains(output.String(), "D dummy%25phrase\nOK\n") {
		t.Fatalf("output=%q error=%v", &output, err)
	}
	if got := <-done; got != "GET_SECRET sandboxed-agents.git-credentials.v1\n" {
		t.Fatalf("request=%q", got)
	}
}

// TestPinentryLockedNeverPrompts proves an absent agent yields an Assuan error without a secret at the pinentry API seam.
func TestPinentryLockedNeverPrompts(t *testing.T) {
	var output bytes.Buffer
	gitcredentials.Pinentry(strings.NewReader("GETPIN\nBYE\n"), &output, filepath.Join(t.TempDir(), "absent"))
	if !strings.Contains(output.String(), "ERR ") || strings.Contains(output.String(), "D ") {
		t.Fatalf("output=%q", &output)
	}
}
