package manager_test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// TestCodexProxyChangeExcludesRuns proves a staged proxy change refuses managed readers before their process starts through RunWithEffects.
func TestCodexProxyChangeExcludesRuns(t *testing.T) {
	e, _ := codexPublicationEffects(t)
	stage := e.Start
	checked := false
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" {
			reader := e
			reader.Start = func(manager.Process) error { t.Fatal("reader started during publication"); return nil }
			if out := runAgents(t, reader, 1, "run", "codex"); !strings.Contains(out, "proxy change is in progress") {
				t.Fatal(out)
			}
			checked = true
		}
		return stage(p)
	}
	runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !checked {
		t.Fatal("staged login did not exercise reader admission")
	}
}

// TestCodexRunsShareConfigurationLock proves multiple runs and unrelated agent changes remain available through RunWithEffects.
func TestCodexRunsShareConfigurationLock(t *testing.T) {
	e, _ := codexPublicationEffects(t)
	home, err := e.HomeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	version, installs := "1.2.3", 0
	unrelated := agentEffects(t, home, &version, &installs)
	starts := 0
	e.Start = func(manager.Process) error {
		starts++
		reader := e
		reader.Start = func(manager.Process) error { starts++; return nil }
		runAgents(t, reader, 0, "run", "codex")
		runAgents(t, unrelated, 0, "enable", "claude")
		return nil
	}
	runAgents(t, e, 0, "run", "codex")
	if starts != 2 || installs != 1 {
		t.Fatalf("runs=%d unrelated installs=%d", starts, installs)
	}
}

// TestCodexProxyRefusesRunningSession proves setup and removal preserve files and read no key while a managed session exists through RunWithEffects.
func TestCodexProxyRefusesRunningSession(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup", true: "remove"}[remove], func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			stage := e.Start
			originals := map[string][]byte{}
			for _, name := range []string{"config.toml", "auth.json"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				originals[name] = data
			}
			e.Stdin = forbiddenProxyInput{t}
			e.Start = func(p manager.Process) error {
				if p.Program != "tmux" || tmuxCommand(p.Args) != "list-sessions" {
					t.Fatal("proxy started while session runs")
				}
				_, err := io.WriteString(p.Stdout, "codex\n")
				return err
			}
			args := []string{"login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen"}
			if remove {
				args = []string{"login", "codex", "proxy", "--remove"}
			}
			if out := runAgents(t, e, 1, args...); !strings.Contains(out, "agents session NAME codex --stop") {
				t.Fatal(out)
			}
			for name, want := range originals {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != string(want) {
					t.Fatalf("running session did not preserve %s", name)
				}
			}
			e.Start, e.Stdin = stage, strings.NewReader("proxy-key")
			runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
		})
	}
}

// TestCodexSessionStartRechecksRecoveryUnderLock proves a marker created during the unlocked session query prevents creation through RunWithEffects.
func TestCodexSessionStartRechecksRecoveryUnderLock(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	queries := 0
	e.Start = func(p manager.Process) error {
		if p.Program != "tmux" || tmuxCommand(p.Args) != "list-sessions" {
			t.Fatal("session started with incomplete publication")
		}
		queries++
		return os.Mkdir(filepath.Join(dir, ".sandboxed-agents-proxy"), 0700)
	}
	if out := runAgents(t, e, 1, "session", "codex"); !strings.Contains(out, "requires recovery") {
		t.Fatal(out)
	}
	if queries != 1 {
		t.Fatalf("expected only the initial session query, got %d", queries)
	}
}

// TestCodexInterruptedRunRequiresExplicitRecovery proves a child surviving an interrupted manager keeps publication refused through RunWithEffects.
func TestCodexInterruptedRunRequiresExplicitRecovery(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	reader := e
	child := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; read line || :")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	reader.Start = func(manager.Process) error {
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		ready, err := bufio.NewReader(output).ReadString('\n')
		if err != nil || ready != "ready\n" {
			t.Fatal("child did not reach its blocking read")
		}
		panic("manager interrupted before child completion")
	}
	interrupted := false
	func() {
		defer func() { interrupted = recover() != nil }()
		runAgents(t, reader, 0, "run", "codex")
	}()
	if !interrupted {
		t.Fatal("run interruption was not reached")
	}
	if out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen"); !strings.Contains(out, "interrupted managed Codex run") {
		t.Fatal(out)
	}
	markers := filepath.Join(filepath.Dir(dir), ".local", "share", "sandboxed-agents", "codex-runs")
	entries, err := os.ReadDir(markers)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one retained run marker: %v (%v)", entries, err)
	}
	input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	// The child is now confirmed stopped; clearing its retained marker models manual recovery.
	if err := os.Remove(filepath.Join(markers, entries[0].Name())); err != nil {
		t.Fatal(err)
	}
	e.Stdin = strings.NewReader("proxy-key")
	runAgents(t, e, 0, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
}

// TestCodexRunMarkersTrackCompletion proves normal completion clears only the current run's marker while uncertain exits retain it through RunWithEffects.
func TestCodexRunMarkersTrackCompletion(t *testing.T) {
	for _, tc := range []struct {
		name            string
		err             error
		status, markers int
	}{
		{"success", nil, 0, 1},
		{"normal nonzero", childExit(23), 23, 1},
		{"signal", childExit(-1), 1, 2},
		{"unknown error", io.ErrUnexpectedEOF, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, dir := codexPublicationEffects(t)
			markers := filepath.Join(filepath.Dir(dir), ".local", "share", "sandboxed-agents", "codex-runs")
			if err := os.MkdirAll(markers, 0700); err != nil {
				t.Fatal(err)
			}
			prior := filepath.Join(markers, "earlier-run")
			if err := os.WriteFile(prior, nil, 0600); err != nil {
				t.Fatal(err)
			}
			e.Start = func(manager.Process) error {
				entries, err := os.ReadDir(markers)
				if err != nil || len(entries) != 2 {
					t.Fatalf("run started before admission was recorded: %v (%v)", entries, err)
				}
				return tc.err
			}
			runAgents(t, e, tc.status, "run", "codex")
			entries, err := os.ReadDir(markers)
			if err != nil || len(entries) != tc.markers {
				t.Fatalf("unexpected retained markers: %v (%v)", entries, err)
			}
			if _, err := os.Stat(prior); err != nil {
				t.Fatal("run removed another run's marker")
			}
		})
	}
}

// TestCodexRunMarkerCreationFailureRefusesStart proves unreadable admission state cannot silently bypass run tracking through RunWithEffects.
func TestCodexRunMarkerCreationFailureRefusesStart(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	markers := filepath.Join(filepath.Dir(dir), ".local", "share", "sandboxed-agents", "codex-runs")
	if err := os.WriteFile(markers, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.Start = func(manager.Process) error { t.Fatal("run started without admission marker"); return nil }
	if out := runAgents(t, e, 1, "run", "codex"); !strings.Contains(out, "requires recovery") {
		t.Fatal(out)
	}
}

// TestCodexProxyRefusesForeignRunState proves any unknown marker entry blocks proxy changes without reading a key through RunWithEffects.
func TestCodexProxyRefusesForeignRunState(t *testing.T) {
	e, dir := codexPublicationEffects(t)
	unknown := filepath.Join(filepath.Dir(dir), ".local", "share", "sandboxed-agents", "codex-runs", "private-key")
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	e.Stdin = forbiddenProxyInput{t}
	e.Start = func(manager.Process) error { t.Fatal("proxy started with unknown run state"); return nil }
	out := runAgents(t, e, 1, "login", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if !strings.Contains(out, "requires recovery") || strings.Contains(out, "private-key") {
		t.Fatal(out)
	}
}
