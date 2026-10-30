package manager_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// execTerminalEnvironment returns a modelled environment for the manager under podman exec -t inside the sandbox:
// TERM=xterm, whose terminfo entry declares eight colours, and no locale variable, so the C locale applies. It is an
// assumed worst case for the attach flags, not an environment recorded from a real sandbox or from the #114 report.
func execTerminalEnvironment(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "TERM=xterm"}
}

// TestAgentSessionDrawsFullScreenThroughLocalTmux proves an attached agent session draws the agent's Unicode glyphs and
// colours, passes typed input to the agent and shows its redraw, and redraws at the new size after a resize, through
// RunWithEffects, the tmux arguments the manager passes, a real local tmux, and a pseudo-terminal of 120 columns and 40
// rows with the modelled podman exec -t environment of execTerminalEnvironment. The agent is a stand-in script and the
// tmux is the host's, so the test reproduces the symptoms of that environment, not the reporter's agent, terminal, or
// image.
func TestAgentSessionDrawsFullScreenThroughLocalTmux(t *testing.T) {
	home, version, installs := t.TempDir(), "1.2.3", 0
	tmuxPath, localArgs := localTmux(t, home)
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "codex")
	// The agent clears its screen and draws a logo of block glyphs in a grey 24-bit colour, as full-screen agents do,
	// redraws it with each line of input it reads, and records each size its terminal reports.
	script := `#!/bin/sh
trap 'stty size > size' WINCH
draw() { printf '\033[H\033[2J\033[38;2;128;128;128m\342\226\210\342\226\200 logo\033[0m\n%s\n' "$1"; }
draw ready
stty size > size
while :; do
	if IFS= read -r line; then draw "input $line"; fi
done
`
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	attaches := 0
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" || p.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("process=%+v", p)
		}
		args := localArgs(p.Args)
		if tmuxCommand(args) == "attach-session" {
			attaches++
			attachThroughTerminal(t, tmuxPath, args, home)
			return nil
		}
		cmd := exec.Command(tmuxPath, args...)
		cmd.Dir = home
		cmd.Env = execTerminalEnvironment(home)
		cmd.Stdout, cmd.Stderr = p.Stdout, p.Stderr
		return cmd.Run()
	}
	runAgents(t, e, 0, "session", "codex")
	if attaches != 1 {
		t.Fatalf("attaches=%d", attaches)
	}
}

// attachThroughTerminal runs the attach command under a 120 by 40 pseudo-terminal, checks the drawn screen, types a line
// and checks the agent's redraw of it, resizes the terminal to 100 by 30, checks the agent's new size and the redraw, and
// detaches with the default prefix key.
func attachThroughTerminal(t *testing.T, tmuxPath string, args []string, home string) {
	t.Helper()
	master, slave := openTerminal(t, 40, 120)
	defer master.Close()
	cmd := exec.Command(tmuxPath, args...)
	cmd.Env = execTerminalEnvironment(home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	var mutex sync.Mutex
	var screen bytes.Buffer
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			mutex.Lock()
			screen.Write(buffer[:n])
			mutex.Unlock()
			if err != nil {
				return
			}
		}
	}()
	drawn := func() string {
		mutex.Lock()
		defer mutex.Unlock()
		return screen.String()
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	// The logo glyphs are U+2588 FULL BLOCK and U+2580 UPPER HALF BLOCK.
	logo := "\u2588\u2580 logo"
	// tmux reduces a 24-bit colour to the nearest of 256 when the terminal declares no 24-bit support; without -2, the
	// local reproduction with the eight-colour xterm entry received plain white instead.
	grey := "\x1b[38;5;244m"
	// The tmux status line takes the last row, so the agent's pane is one row shorter than the terminal.
	waitForTerminal(t, func() bool { return strings.Contains(drawn(), "logo") && agentSize(home) == "39 120" }, drawn, "first screen at 120 by 40")
	if first := drawn(); !strings.Contains(first, logo) || !strings.Contains(first, grey) {
		t.Fatalf("first screen lacks the logo glyphs %q in grey %q: %q", logo, grey, first)
	}
	beforeInput := len(drawn())
	if _, err := master.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, func() bool { return strings.Contains(drawn()[beforeInput:], "input hello") }, drawn, "redraw after input")
	if redrawn := drawn()[beforeInput:]; !strings.Contains(redrawn, logo) || !strings.Contains(redrawn, grey) {
		t.Fatalf("redraw after input lacks the logo glyphs %q in grey %q: %q", logo, grey, redrawn)
	}
	beforeResize := len(drawn())
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, func() bool { return agentSize(home) == "29 100" && strings.Contains(drawn()[beforeResize:], logo) }, drawn, "redraw at 100 by 30")
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("attached client ended before detach: %v", err)
	}
	if _, err := master.Write([]byte("\x02d")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("detach: %v: %q", err, drawn())
	}
}

// agentSize returns the last rows and columns the agent recorded from its terminal, or an empty string before the first.
func agentSize(home string) string {
	size, _ := os.ReadFile(filepath.Join(home, "size"))
	return strings.TrimSpace(string(size))
}

// waitForTerminal polls the condition for up to five seconds and fails with the drawn output when it never holds.
func waitForTerminal(t *testing.T, condition func() bool, drawn func() string, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !condition(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear: %q", what, drawn())
		}
	}
}

// openTerminal returns the master and slave ends of a fresh pseudo-terminal of the given rows and columns.
func openTerminal(t *testing.T, rows, columns uint16) (*os.File, *os.File) {
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
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: columns}); err != nil {
		master.Close()
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	return master, slave
}
