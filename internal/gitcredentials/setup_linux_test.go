package gitcredentials_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestWrongPassphraseRelocksStalledAgent proves a failed unlock removes an earlier unlock even if the old agent cannot answer IPC at the Store API seam.
func TestWrongPassphraseRelocksStalledAgent(t *testing.T) {
	s := realStore(t)
	phrase := []byte("stalled-unlock-dummy")
	if err := s.Setup(phrase, phrase); err != nil {
		t.Fatal(err)
	}
	input := "protocol=https\nhost=example.invalid\nusername=dummy\npassword=dummy-stalled-pat\n\n"
	if err := s.Helper([]string{"--generic", "store"}, strings.NewReader(input), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	request := "protocol=https\nhost=example.invalid\nusername=dummy\n\n"
	if err := s.Helper([]string{"--generic", "get"}, strings.NewReader(request), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("gpg-connect-agent", "--homedir", s.Home+"/.local/share/sandboxed-agents/git-credentials/gnupg", "GETINFO pid", "/bye")
	out, err := command.Output()
	if err != nil {
		t.Fatal("agent PID query failed")
	}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "D ") {
			pid, _ = strconv.Atoi(strings.TrimPrefix(line, "D "))
		}
	}
	if pid <= 0 {
		t.Fatal("agent PID missing")
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGCONT)
	if err := s.Setup([]byte("wrong-stalled-dummy"), nil); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	syscall.Kill(pid, syscall.SIGCONT)
	if err := s.Helper([]string{"--generic", "get"}, strings.NewReader(request), &bytes.Buffer{}); err == nil {
		t.Fatal("failed unlock retained the old agent's unlock")
	}
}

// TestRelockRejectsReusedProcessIdentity proves an obsolete agent record cannot signal a different process at the Store.Lock seam.
func TestRelockRejectsReusedProcessIdentity(t *testing.T) {
	s := realStore(t)
	phrase := []byte("stale-record-dummy")
	if err := s.Setup(phrase, phrase); err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}

	for _, pid := range []int{os.Getpid(), 1} {
		data := []byte(fmt.Sprintf(`{"PID":%d,"Start":"0","Boot":%q}`, pid, strings.TrimSpace(string(boot))))
		if err := os.WriteFile(s.Home+"/.local/share/sandboxed-agents/git-credentials/agent-process.json", data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.Lock(); err != nil {
			t.Fatalf("stale PID %d: %v", pid, err)
		}
		if err := s.Check(); err == nil {
			t.Fatal("relock retained dedicated agent unlock")
		}
	}

}
