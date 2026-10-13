// Package testutil supplies test-owned external process fakes.
package testutil

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const LiveTestsEnv = "SANDBOXED_AGENTS_LIVE_TESTS"

// Fakes installs a copy of the test executable as podman and ssh in a fresh directory.
// Each executable records argv as JSON lines and accepts scripted output/status via env.
type Fakes struct {
	Dir       string
	PodmanLog string
	SSHLog    string
}

func Install(t *testing.T, executable string) Fakes {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"podman", "ssh"} {
		if err := os.WriteFile(filepath.Join(dir, name+suffix), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return Fakes{Dir: dir, PodmanLog: filepath.Join(dir, "podman.jsonl"), SSHLog: filepath.Join(dir, "ssh.jsonl")}
}

// Env replaces keys, including an explicitly empty value, without duplicate variables.
func Env(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func (f Fakes) Env(base []string) []string {
	return Env(base, map[string]string{
		"PATH":            f.Dir,
		"TEST_PODMAN_LOG": f.PodmanLog, "TEST_SSH_LOG": f.SSHLog,
		"TEST_PODMAN_OUTPUT": "", "TEST_PODMAN_EXIT": "0",
		"TEST_PODMAN_RESPONSES": "", "TEST_PODMAN_ENV_LOG": "",
		"TEST_SSH_OUTPUT": "", "TEST_SSH_EXIT": "0",
		"SANDBOXED_AGENTS_GROUP": "default",
		"XDG_STATE_HOME":         f.Dir, "LOCALAPPDATA": f.Dir,
		LiveTestsEnv: "",
	})
}

func Invocations(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls [][]string
	decoder := json.NewDecoder(file)
	for {
		var args []string
		err := decoder.Decode(&args)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}
