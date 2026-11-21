package manager_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestEnabledAgentSessionSurvivesWhenInstallScriptPlacesCommand proves an agent whose npm package ships a placeholder
// command, which only the package's own install script replaces, draws in the attached terminal, reads typed input,
// follows a resize, and keeps its session after the detach keys, through RunWithEffects enable and session, a real
// local tmux, a pseudo-terminal, and an npm stand-in at the process seam that runs the package's install script only
// when the manager's arguments allow that package's scripts, as npm 12 does. With the placeholder left in place the
// attached terminal shows tmux's "no sessions" instead of the agent's first screen.
func TestEnabledAgentSessionSurvivesWhenInstallScriptPlacesCommand(t *testing.T) {
	home := t.TempDir()
	server := localTmux(t, home)
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	effects.HomeDirectory = func() (string, error) { return home, nil }
	const packageName = "@opencode/cli"
	attaches := 0
	effects.Start = func(process manager.Process) error {
		if process.Identity != (manager.Identity{UID: 1000, GID: 1000}) {
			t.Fatalf("unexpected process identity: %+v", process)
		}
		if process.Program == "tmux" {
			if tmuxCommand(process.Args) == "attach-session" {
				attaches++
				// The agent's own first screen, its redraw of typed input, and its redraw at the new size are the
				// readiness signals; the helper's time limit only ends a run in which they never arrive.
				attachThroughTerminal(t, server, process.Args, home)
				return nil
			}
			return server.run(process, home, execTerminalEnvironment(home))
		}
		separator := slices.Index(process.Args, "--")
		if process.Program != "npm" || separator < 4 || strings.Join(process.Args[:3], " ") != "install --global --prefix" || !slices.Equal(process.Args[separator+1:], []string{packageName}) {
			t.Fatalf("unexpected installation process: %+v", process)
		}
		prefix := process.Args[3]
		packagePath := filepath.Join(prefix, "lib", "node_modules", packageName)
		for _, directory := range []string{filepath.Join(packagePath, "bin"), filepath.Join(prefix, "bin")} {
			if err := os.MkdirAll(directory, 0700); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(packagePath, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":"2.3.2"}`, packageName)), 0600); err != nil {
			return err
		}
		// The package ships this placeholder, which has no interpreter line, and replaces it in its postinstall script.
		command := "echo \"Error: @opencode/cli's postinstall script was not run.\" >&2\nexit 1\n"
		if slices.Contains(process.Args[4:separator], "--allow-scripts="+packageName) {
			// The installed agent draws a logo, redraws it with each line it reads, and records each terminal size.
			command = `#!/bin/sh
trap 'stty size > size; draw "resize $(cat size)"' WINCH
draw() { printf '\033[H\033[2J\033[38;2;128;128;128m\342\226\210\342\226\200 logo\033[0m\n%s\n' "$1"; }
draw ready
stty size > size
while :; do
	if IFS= read -r line; then draw "input $line"; fi
done
`
		}
		if err := os.WriteFile(filepath.Join(packagePath, "bin", "opencode.exe"), []byte(command), 0700); err != nil {
			return err
		}
		return os.Symlink(filepath.Join("..", "lib", "node_modules", packageName, "bin", "opencode.exe"), filepath.Join(prefix, "bin", "opencode"))
	}
	runAgents(t, effects, 0, "enable", "opencode")
	runAgents(t, effects, 0, "session", "opencode")
	// The stand-in agent reads its terminal forever, so after the detach its session is present until stopped.
	if attaches != 1 || !readAgentStatus(t, effects, "opencode").Session {
		t.Fatalf("attaches=%d; agent session missing after the detach keys", attaches)
	}
	runAgents(t, effects, 0, "session", "opencode", "--stop")
	if readAgentStatus(t, effects, "opencode").Session {
		t.Fatal("agent session still present after stop")
	}
	runAgents(t, effects, 0, "disable", "opencode")
}
