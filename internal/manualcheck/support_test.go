package manualcheck_test

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// runtimeIsUnix reports whether file modes are meaningful on the host.
func runtimeIsUnix() bool {
	return runtime.GOOS != "windows"
}

// lookJq finds jq on the original PATH, before any fake directory.
func lookJq() (string, error) {
	return exec.LookPath("jq")
}

// runShell runs a shell script in dir with jq first on PATH and returns the command error.
func runShell(t *testing.T, dir, jq, script string) error {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	command := exec.Command(shell, "-c", script)
	command.Dir = dir
	command.Env = append(os.Environ(), "PATH="+strings.TrimSuffix(jq, "/jq")+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	t.Logf("jq: %s", strings.TrimSpace(string(output)))
	return err
}

// runtimeIsWindowsHost reports whether the tests run on Windows.
func runtimeIsWindowsHost() bool {
	return runtime.GOOS == "windows"
}
