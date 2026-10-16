package main_test

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestLiveTerminalWaitDrainsOutput proves that once Wait reports the command finished, Output already holds everything the command wrote, through the pseudo-terminal helper and the fake process writing a last line right before it exits.
func TestLiveTerminalWaitDrainsOutput(t *testing.T) {
	const last = "last line before exit"
	// More than a pseudo-terminal buffer, so the final write and the exit race the reader.
	filler := strings.Repeat("x", 64*1024)
	// A stressed Linux run (one scheduler thread, loaded CPUs) lost the last
	// line within a few dozen trials before the fix. A pseudo console spawn
	// costs far more than a pseudo-terminal, so Windows runs fewer trials.
	trials := 100
	if runtime.GOOS == "windows" {
		trials = 25
	}
	for attempt := 0; attempt < trials; attempt++ {
		terminal := startLiveTerminal(t, append(os.Environ(), "TEST_FAKE_LOG="+t.TempDir()+"/fake.jsonl", "TEST_FAKE_OUTPUT="+filler+"\n"+last+"\n"), fakeExecutable, "write-and-exit")
		exit, finished := terminal.Wait(20 * time.Second)
		output := terminal.Output()
		terminal.Close()
		if !finished || exit != 0 {
			t.Fatalf("attempt %d: finished=%v exit=%d", attempt, finished, exit)
		}
		if !strings.Contains(output, last) {
			t.Fatalf("attempt %d: Wait returned before the output was drained: %d bytes, last line missing", attempt, len(output))
		}
	}
}

// TestLiveTerminalWaitReportsUndrainedOutputAsUnfinished proves Wait does not report a command finished while a descendant still holds the terminal open, and reports it once the terminal drains, through the pseudo-terminal helper and a shell that exits while a child ignoring hangups keeps its terminal.
func TestLiveTerminalWaitReportsUndrainedOutputAsUnfinished(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the lingering descendant uses a POSIX shell and SIGHUP semantics")
	}
	// The shell and its sleep come from the system directories, not from
	// PATH, which the nested offline guard isolates to the fake programs.
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	environment := testutil.Env(os.Environ(), map[string]string{"PATH": "/usr/bin:/bin"})
	terminal := startLiveTerminal(t, environment, "/bin/sh", "-c", `trap "" HUP; sleep 2 & echo "shell done"; exit 0`)
	defer terminal.Close()
	exit, finished := terminal.Wait(500 * time.Millisecond)
	if finished {
		t.Fatalf("Wait reported the command finished (exit %d) while its descendant still held the terminal", exit)
	}
	// The shell itself has ended by now: its last line is already readable.
	if !strings.Contains(terminal.Output(), "shell done") {
		t.Fatalf("the shell had not ended within the first wait: output=%q", terminal.Output())
	}
	exit, finished = terminal.Wait(10 * time.Second)
	if !finished || exit != 0 || !strings.Contains(terminal.Output(), "shell done") {
		t.Fatalf("after the descendant ended: finished=%v exit=%d output=%q", finished, exit, terminal.Output())
	}
}
