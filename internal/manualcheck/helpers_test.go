package manualcheck_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manualcheck"
)

// splitLines splits text into lines without a trailing empty line.
func splitLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// joinLines joins lines with newlines.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// response is one scripted answer of the process fake for an argv key.
type response struct {
	Output    string `json:",omitempty"`
	Stderr    string `json:",omitempty"`
	Exit      int    `json:",omitempty"`
	WaitFor   string `json:",omitempty"`
	ReadyFile string `json:",omitempty"`
	Sequence  []struct {
		Output string `json:",omitempty"`
		Exit   int    `json:",omitempty"`
	} `json:",omitempty"`
}

// fakeTools holds one directory of process fakes named after the tools a runner calls.
type fakeTools struct {
	dir   string
	names []string
}

// fakeExecutable is the process fake built once per test binary by TestMain.
var fakeExecutable string

// TestMain builds the process fake once, in a directory that outlives every test, and removes it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "manualcheck-fake")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, "process-fake")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	build := exec.Command("go", "build", "-o", path, filepath.Join("..", "testutil", "testdata", "fake.go"))
	if output, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("build process fake: " + err.Error() + "\n" + string(output))
	}
	fakeExecutable = path
	status := m.Run()
	os.RemoveAll(dir)
	os.Exit(status)
}

// fake builds the process fake and installs it under the given tool names in a fresh directory
// that is placed first on PATH; responses are scripted per tool through environment variables.
func fake(t *testing.T, names ...string) *fakeTools {
	t.Helper()
	data, err := os.ReadFile(fakeExecutable)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), data, 0700); err != nil {
			t.Fatal(err)
		}
		prefix := envPrefix(name)
		t.Setenv(prefix+"_LOG", filepath.Join(dir, name+".jsonl"))
		t.Setenv(prefix+"_OUTPUT", "")
		t.Setenv(prefix+"_EXIT", "0")
		t.Setenv(prefix+"_RESPONSES", "")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeTools{dir: dir, names: names}
}

// envPrefix returns the environment prefix the fake derives from its executable name.
func envPrefix(name string) string {
	return "TEST_" + strings.ReplaceAll(strings.ToUpper(name), "-", "_")
}

// path returns the executable path of a tool fake.
func (f *fakeTools) path(name string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(f.dir, name+".exe")
	}
	return filepath.Join(f.dir, name)
}

// script sets the scripted responses of one tool fake, keyed by its argument line, with `*` as
// a wildcard.
func (f *fakeTools) script(t *testing.T, name string, responses map[string]response) {
	t.Helper()
	data, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPrefix(name)+"_RESPONSES", string(data))
}

// calls returns the recorded argument lines of a tool fake.
func (f *fakeTools) calls(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name+".jsonl"))
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range splitLines(string(data)) {
		var args []string
		if json.Unmarshal([]byte(line), &args) == nil {
			lines = append(lines, strings.Join(args, " "))
		}
	}
	return lines
}

// verifiedRun writes a run.json for a fake executable and returns the loaded run, which writes its
// progress to the test log.
func verifiedRun(t *testing.T, f *fakeTools, executable string) *manualcheck.Run {
	t.Helper()
	parent := t.TempDir()
	root, err := manualcheck.RunRoot(parent, "test-run")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := manualcheck.FileSHA256(executable)
	if err != nil {
		t.Fatal(err)
	}
	run := &manualcheck.Run{SchemaVersion: 1, ID: "test-run", Commit: strings.Repeat("a", 40), Version: "1.0.0-preview.20260101.1",
		AssetSHA256: strings.Repeat("b", 64), Executable: executable, ExecutableSHA256: sha, Platform: manualcheck.PlatformLinux, Group: "manual-check", Root: root}
	if err := run.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := manualcheck.LoadRun(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Stdout = &testWriter{t: t}
	return loaded
}

// testWriter sends runner output to the test log.
type testWriter struct{ t *testing.T }

// Write logs the written text.
func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// readFile reads a file of the run directory or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
