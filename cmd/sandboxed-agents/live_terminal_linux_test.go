//go:build linux

package main_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

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

// Wait returns the exit status once the command has ended within timeout and
// its output has been drained: the master reads everything the command wrote
// and then fails with EIO once no process holds the slave any more. A
// descendant that keeps the slave open bounds the drain to the timeout.
func (terminal *liveTerminal) Wait(timeout time.Duration) (int, bool) {
	deadline := time.After(timeout)
	select {
	case <-terminal.done:
	case <-deadline:
		return 0, false
	}
	select {
	case <-terminal.readerDone:
	case <-deadline:
	}
	return terminal.exit, true
}

// Close ends a still running command and releases the terminal.
func (terminal *liveTerminal) Close() {
	select {
	case <-terminal.done:
	default:
		// The command leads its own session, so its group covers the podman
		// exec it may have started; ending the whole group frees the terminal.
		unix.Kill(-terminal.command.Process.Pid, unix.SIGKILL)
		<-terminal.done
	}
	terminal.master.Close()
}
