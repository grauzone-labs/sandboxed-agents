package cli_test

import (
	"os"
	"path/filepath"
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

// TestInterruptWaitingImageLockReleasesLifecycle proves SIGINT ends an up blocked
// on an image-tag lock without creating sandbox objects and lets the same sandbox
// retry, through the Linux subprocess CLI, kernel lock inventory and fake Podman seams.
func TestInterruptWaitingImageLockReleasesLifecycle(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	holder := startConcurrentCommand(t, f, "default", "build")
	waitHandshakeFile(t, ready)
	lockPath := imageTagLockPath(baseTag(t))
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	waiter := startWaitingUp(t, f, "other", "demo", lockPath)
	if err := waiter.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waiter.finish(t, -1)
	assertWaitingNotice(t, waiter, "image-tag")
	if countPodmanCalls(t, f, "volume create ") != 0 || countPodmanCalls(t, f, "create ") != 0 {
		t.Fatal("interrupted lock waiter created sandbox objects")
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("interrupt removed or replaced persistent lock: %v", err)
	}
	releaseHandshakeFile(t, release)
	holder.finish(t, 0)
	retry := startConcurrentCommand(t, f, "other", "up", "demo")
	retry.finish(t, 0)
	assertWaitingNotice(t, retry, "")
	if countPodmanCalls(t, f, "volume create ") != 3 || countPodmanCalls(t, f, "create ") != 1 {
		t.Fatal("retry did not acquire released lifecycle and image locks exactly once")
	}
}
