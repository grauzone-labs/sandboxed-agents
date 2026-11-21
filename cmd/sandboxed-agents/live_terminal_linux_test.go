//go:build linux

package main_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"golang.org/x/sys/unix"
)

// liveTerminal runs one command whose standard streams are a pseudo-terminal
// the test owns, as a user's terminal would.
type liveTerminal struct {
	master     *os.File
	command    *exec.Cmd
	mutex      sync.Mutex
	output     bytes.Buffer
	done       chan struct{}
	readerDone chan struct{}
	exit       int
	// exited and drained are written before done and readerDone close.
	exited, drained time.Time
}

// startLiveTerminal starts program under a new session whose controlling
// terminal is a fresh pseudo-terminal of 120 columns and 40 rows.
func startLiveTerminal(t *testing.T, environment []string, program string, args ...string) *liveTerminal {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	// Raw mode, as an attached session puts its terminal into: keystrokes such
	// as the tmux detach prefix reach the command at once, without a line end.
	settings, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		slave.Close()
		master.Close()
		t.Fatal(err)
	}
	settings.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	settings.Oflag &^= unix.OPOST
	settings.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	settings.Cflag &^= unix.CSIZE | unix.PARENB
	settings.Cflag |= unix.CS8
	settings.Cc[unix.VMIN], settings.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, settings); err != nil {
		slave.Close()
		master.Close()
		t.Fatal(err)
	}
	command := exec.Command(program, args...)
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := command.Start(); err != nil {
		slave.Close()
		master.Close()
		t.Fatal(err)
	}
	// The child holds the only remaining slave descriptor, so reads on the
	// master end when the child and its descendants have exited.
	slave.Close()
	terminal := &liveTerminal{master: master, command: command, done: make(chan struct{}), readerDone: make(chan struct{})}
	go func() {
		defer close(terminal.readerDone)
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				terminal.mutex.Lock()
				terminal.output.Write(buffer[:n])
				terminal.mutex.Unlock()
			}
			if err != nil {
				terminal.drained = time.Now()
				return
			}
		}
	}()
	go func() {
		err := command.Wait()
		if exit, ok := err.(*exec.ExitError); ok {
			terminal.exit = exit.ExitCode()
		} else if err != nil {
			terminal.exit = -1
		}
		terminal.exited = time.Now()
		close(terminal.done)
	}()
	return terminal
}

// Write sends keystrokes to the command as typed input.
func (terminal *liveTerminal) Write(input string) error {
	_, err := terminal.master.Write([]byte(input))
	return err
}

// Output returns everything the command has written to the terminal so far.
func (terminal *liveTerminal) Output() string {
	terminal.mutex.Lock()
	defer terminal.mutex.Unlock()
	return terminal.output.String()
}

// outputSince returns only the bytes written after the first cursor bytes; the
// captured output only grows, so earlier bytes are never copied again.
func (terminal *liveTerminal) outputSince(cursor int) string {
	terminal.mutex.Lock()
	defer terminal.mutex.Unlock()
	if data := terminal.output.Bytes(); cursor < len(data) {
		return string(data[cursor:])
	}
	return ""
}

// Wait returns the exit status once the command has ended within timeout and
// its output has been drained: the master reads everything the command wrote
// and then fails with EIO once no process holds the slave any more. While a
// descendant keeps the slave open the output may still grow, so Wait reports
// the command as unfinished until the terminal has drained.
func (terminal *liveTerminal) Wait(timeout time.Duration) (int, bool) {
	deadline := time.After(timeout)
	select {
	case <-terminal.done:
	case <-deadline:
		return 0, false
	}
	select {
	case <-terminal.readerDone:
		return terminal.exit, true
	case <-deadline:
		return 0, false
	}
}

// completedAt reports, without blocking, when the command had both exited and
// drained its terminal, the moment Wait first reports it finished.
func (terminal *liveTerminal) completedAt() (time.Time, bool) {
	select {
	case <-terminal.done:
	default:
		return time.Time{}, false
	}
	select {
	case <-terminal.readerDone:
	default:
		return time.Time{}, false
	}
	if terminal.drained.After(terminal.exited) {
		return terminal.drained, true
	}
	return terminal.exited, true
}

// Close ends a still running command, or the owned descendants that still hold
// the terminal after the command exited, and releases the terminal.
func (terminal *liveTerminal) Close() {
	select {
	case <-terminal.readerDone:
	default:
		// The command leads its own session, so its group covers the podman
		// exec it may have started; ending the whole group frees the terminal.
		// A descendant that ignores hangups keeps the group, and so its ID,
		// alive after the command exits; it must end here as well.
		unix.Kill(-terminal.command.Process.Pid, unix.SIGKILL)
	}
	<-terminal.done
	// Closing the master ends the reader; wait for it so nothing appends to
	// the output after Close returns.
	terminal.master.Close()
	select {
	case <-terminal.readerDone:
	case <-time.After(5 * time.Second):
	}
}

// TestLiveTerminalLingerBoundFixture exercises a class timeout whose command exits
// while a descendant ignoring hangups keeps the terminal; its parent expects the
// reported deadline failure and checks that the descendant ended.
func TestLiveTerminalLingerBoundFixture(t *testing.T) {
	pidFile := os.Getenv("TEST_LIVE_TERMINAL_LINGER")
	if pidFile == "" {
		return
	}
	host := liveHost{binary: "/bin/sh", environment: testutil.Env(os.Environ(), map[string]string{"PATH": "/usr/bin:/bin"}), operationBounds: map[string]time.Duration{"inspection": 500 * time.Millisecond}, report: func(line string) { t.Log(line) }}
	terminal := host.startTerminal(t, "-c", `trap "" HUP; sleep 30 & echo $! > "$TEST_LIVE_TERMINAL_LINGER"; echo shell done; exit 0`)
	defer terminal.Close()
	terminal.Wait(5 * time.Second)
}

// TestLiveTerminalClassBoundEndsLingeringDescendant proves a class timeout ends
// the owned process group even after the command itself exited, so a descendant
// that ignores hangups and keeps the terminal stops, through the timed terminal
// seam in a nested fixture and the descendant's process state.
func TestLiveTerminalClassBoundEndsLingeringDescendant(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	values := nestedExecutables()
	values["TEST_LIVE_TERMINAL_LINGER"] = pidFile
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveTerminalLingerBoundFixture$")
	command.Env = testutil.Env(os.Environ(), values)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "inspection exceeded class bound") {
		t.Fatalf("terminal fixture: %v\n%q", err, output)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid descendant pid %q", data)
	}
	deadline := time.Now().Add(5 * time.Second)
	for liveProcessRunning(pid) {
		if time.Now().After(deadline) {
			unix.Kill(pid, unix.SIGKILL)
			t.Fatalf("class timeout left descendant %d holding the terminal", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// liveProcessRunning reports whether pid names a process that has neither exited
// nor become a zombie awaiting its reaper.
func liveProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	_, fields, found := strings.Cut(string(data), ") ")
	return found && !strings.HasPrefix(fields, "Z") && !strings.HasPrefix(fields, "X")
}
