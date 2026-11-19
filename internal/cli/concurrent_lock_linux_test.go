package cli_test

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitHostLock proves the CLI subprocess is blocked in the kernel on the expected file,
// rather than merely not scheduled yet, through Linux's external lock inventory.
func waitHostLock(t *testing.T, run *concurrentCommand, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	inode := strconv.FormatUint(info.Sys().(*syscall.Stat_t).Ino, 10)
	pid := strconv.Itoa(run.command.Process.Pid)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		locks, err := os.ReadFile("/proc/locks")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(locks), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 9 && fields[1] == "->" && fields[2] == "FLOCK" && fields[5] == pid && strings.HasSuffix(fields[6], ":"+inode) {
				return
			}
		}
		select {
		case err := <-run.done:
			t.Fatalf("CLI exited instead of waiting for %s: %v; output=%s", path, err, run.output.String())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("CLI pid %s never blocked on %s", pid, path)
}
