package main_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// TestLiveTerminalFramesPayloadLines proves terminal deltas and the final partial
// line stay escaped and reconstruct the exact captured output at the terminal seam.
func TestLiveTerminalFramesPayloadLines(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("terminal support requires Linux or Windows")
	}
	var records []string
	environment := append(os.Environ(), "TEST_FAKE_LOG="+filepath.Join(t.TempDir(), "calls.jsonl"), "TEST_FAKE_OUTPUT=first\nlive operation image build: started\npartial")
	host := liveHost{binary: fakeExecutable, environment: environment, report: func(line string) { records = append(records, line) }}
	terminal := host.startTerminal(t, "write-and-exit")
	defer terminal.Close()
	exit, finished := terminal.Wait(20 * time.Second)
	output := terminal.Output()
	if !finished || exit != 0 {
		t.Fatalf("terminal did not complete: finished=%v exit=%d output=%q", finished, exit, output)
	}
	var payload strings.Builder
	for _, record := range records[1 : len(records)-1] {
		quoted, ok := strings.CutPrefix(record, "live output: ")
		if !ok || strings.ContainsAny(record, "\r\n") {
			t.Fatalf("unframed terminal payload: %q", record)
		}
		fragment, err := strconv.Unquote(quoted)
		if err != nil || len(fragment) > 4096 {
			t.Fatalf("invalid terminal record: %q err=%v", record, err)
		}
		payload.WriteString(fragment)
	}
	if !strings.Contains(output, "live operation image build: started") || payload.String() != output {
		t.Fatalf("terminal payload changed: captured=%q streamed=%q", output, payload.String())
	}
	if records[0] != "live operation inspection: started" || !strings.HasPrefix(records[len(records)-1], "live operation inspection: finished after ") {
		t.Fatalf("host control records changed: %q", records)
	}
}

// TestLiveTerminalLateWaitKeepsOnTimeResult proves an invocation that completed
// before its class deadline keeps its own passing result when the first Wait runs
// after that deadline, through the timed terminal seam and the fake process.
func TestLiveTerminalLateWaitKeepsOnTimeResult(t *testing.T) {
	if livePlatform() == "" {
		t.Skip("terminal support requires Linux or Windows")
	}
	const bound = 2 * time.Second
	environment := append(os.Environ(), "TEST_FAKE_LOG="+filepath.Join(t.TempDir(), "calls.jsonl"), "TEST_FAKE_OUTPUT=done\n")
	host := liveHost{binary: fakeExecutable, environment: environment, timings: &liveTimings{}, operationBounds: map[string]time.Duration{"inspection": bound}}
	terminal := host.startTerminal(t, "write-and-exit")
	defer terminal.Close()
	for {
		if completed, ok := terminal.liveTerminal.completedAt(); ok {
			if completed.After(terminal.started.Add(bound)) {
				t.Skip("the fake process did not complete within its class bound on this host")
			}
			break
		}
		if time.Since(terminal.started) > bound {
			t.Skip("the fake process did not complete within its class bound on this host")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Until(terminal.started.Add(bound + 100*time.Millisecond)))
	exit, finished := terminal.Wait(5 * time.Second)
	if !finished || exit != 0 {
		t.Fatalf("on-time invocation became a timeout: finished=%v exit=%d", finished, exit)
	}
	if timings := host.timings.evidence.Operations; len(timings) != 1 || timings[0].Result != "pass" {
		t.Fatalf("on-time invocation timing: %+v", timings)
	}
}
