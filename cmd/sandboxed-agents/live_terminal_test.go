package main_test

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
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
