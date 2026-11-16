package gitcredentials

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHelperBudgetSpansProcesses proves sequential helper stages share a deadline and expired stages never start at the subprocess seam.
func TestHelperBudgetSpansProcesses(t *testing.T) {
	start := time.Now()
	s := Store{Home: t.TempDir(), deadline: start.Add(200 * time.Millisecond)}
	if err := s.runWithDiagnostics("/bin/sleep", []string{"0.12"}, nil, io.Discard, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	err := s.runWithDiagnostics("/bin/sleep", []string{"0.20"}, nil, io.Discard, io.Discard, nil)
	if !errors.Is(err, errHelperDeadline) {
		t.Fatal("second stage received a fresh budget")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond+KillGrace+200*time.Millisecond {
		t.Fatalf("shared budget exceeded: %v", elapsed)
	}
	marker := filepath.Join(s.Home, "started")
	err = s.runWithDiagnostics("/usr/bin/touch", []string{marker}, nil, io.Discard, io.Discard, nil)
	if !errors.Is(err, errHelperDeadline) {
		t.Fatal("expired stage did not fail")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired stage started a subprocess")
	}
}

// TestHelperBudgetSpansAgentExchanges proves greeting and command IO cannot reset a shared helper deadline at the Unix socket seam.
func TestHelperBudgetSpansAgentExchanges(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(80 * time.Millisecond)
		io.WriteString(c, "OK\n")
		buffer := make([]byte, 128)
		c.Read(buffer)
		time.Sleep(100 * time.Millisecond)
		io.WriteString(c, "D dummy\nOK\n")
	}()
	began := time.Now()
	a, err := connectAgentBefore(socket, began.Add(120*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.command("GET_SECRET test-only", nil); err == nil {
		t.Fatal("command received a new socket budget")
	}
	if elapsed := time.Since(began); elapsed > 200*time.Millisecond {
		t.Fatalf("socket exceeded shared budget: %v", elapsed)
	}
	<-done
}
