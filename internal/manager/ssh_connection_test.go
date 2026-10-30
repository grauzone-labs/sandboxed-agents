package manager_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// sshFlowEvent records, in order, what the SSH integration did at the manager seam: printed output, read an answer, or
// started a program.
type sshFlowEvent struct {
	kind    string
	process manager.Process
}

// sshFlowRecorder appends an event for every write to a stream or read from the answers, so tests can order the
// approval question against output and process starts.
type sshFlowRecorder struct {
	events *[]sshFlowEvent
	kind   string
	target io.ReadWriter
}

// Read records one answer read and returns bytes from the scripted answers.
func (r sshFlowRecorder) Read(data []byte) (int, error) {
	*r.events = append(*r.events, sshFlowEvent{kind: r.kind})
	return r.target.Read(data)
}

// Write records one write and keeps the bytes for later inspection.
func (r sshFlowRecorder) Write(data []byte) (int, error) {
	*r.events = append(*r.events, sshFlowEvent{kind: r.kind})
	return r.target.Write(data)
}

// runSSHFlow runs the SSH integration with scripted answers on standard input and returns its status, standard output,
// diagnostics, and the ordered events of public output, answer reads, and process starts. connect answers every
// connection test the flow starts through /usr/bin/ssh; key processes run the local ssh-keygen as sshKeyEffects does.
func runSSHFlow(t *testing.T, home, host, user, answers string, connect func(manager.Process) error) (int, string, string, []sshFlowEvent) {
	t.Helper()
	var calls []manager.Process
	var events []sshFlowEvent
	e := sshKeyEffects(t, home, &calls)
	keygen := e.Start
	e.Start = func(p manager.Process) error {
		events = append(events, sshFlowEvent{kind: "start", process: p})
		if p.Program == "/usr/bin/ssh-keygen" {
			return keygen(p)
		}
		if connect == nil {
			t.Fatalf("unexpected process: %+v", p)
		}
		return connect(p)
	}
	var out, diag bytes.Buffer
	e.Stdin = sshFlowRecorder{events: &events, kind: "answer", target: bytes.NewBufferString(answers)}
	status := manager.RunWithEffects([]string{"integrations", "config", "ssh", "--host", host, "--user", user}, sshFlowRecorder{events: &events, kind: "public", target: &out}, &diag, e)
	return status, out.String(), diag.String(), events
}

// TestSSHIntegrationAsksApprovalBeforeAnyConnection proves the flow prints the public key, then asks for approval of the
// connection test, and that declining, an empty answer, and end of input make no connection, write no host entry, retain
// the dedicated key, say so, and exit non-zero, through RunWithEffects with scripted answers and recorded process starts.
func TestSSHIntegrationAsksApprovalBeforeAnyConnection(t *testing.T) {
	enableSSHIntegration(t)
	for _, c := range []struct{ name, answers, says string }{
		{"no", "no\n", "SSH connection test declined"},
		{"n", "N\n", "SSH connection test declined"},
		{"empty", "\n", "SSH connection test declined"},
		{"reasked", "maybe\nno\n", "SSH connection test declined"},
		{"end of input", "", "SSH connection test cancelled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			status, out, diag, events := runSSHFlow(t, home, "build.example", "alice", c.answers, nil)
			if status != 1 || !strings.HasPrefix(out, "ssh-ed25519 ") {
				t.Fatalf("status=%d out=%q diagnostics=%s", status, out, diag)
			}
			if !strings.Contains(diag, "alice@build.example") || !strings.Contains(diag, c.says) || !strings.Contains(diag, "no connection made") || !strings.Contains(diag, "no host entry written") {
				t.Fatalf("diagnostics do not name the question and its outcome: %s", diag)
			}
			if c.name == "reasked" && strings.Count(diag, "alice@build.example with password authentication") != 2 {
				t.Fatalf("unrecognized answer was not asked again: %s", diag)
			}
			printed, asked := -1, -1
			for i, event := range events {
				switch {
				case event.kind == "public" && printed < 0:
					printed = i
				case event.kind == "answer" && asked < 0:
					asked = i
				case event.kind == "start" && asked >= 0:
					t.Fatalf("process started after the approval question: %+v", event.process)
				}
			}
			if printed < 0 || asked < printed {
				t.Fatalf("approval asked before the public key was printed: %+v", events)
			}
			keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_ed25519_remote_*"))
			if len(keys) != 2 || !strings.Contains(diag, "dedicated key retained at "+strings.TrimSuffix(keys[0], ".pub")) {
				t.Fatalf("keys=%q diagnostics=%s", keys, diag)
			}
			for _, name := range []string{"config", "known_hosts"} {
				if _, err := os.Lstat(filepath.Join(home, ".ssh", name)); !os.IsNotExist(err) {
					t.Fatalf("%s written after %s: %v", name, c.name, err)
				}
			}
		})
	}
}
