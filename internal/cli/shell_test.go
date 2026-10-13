package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

const shellContainer = "sandboxed-agents.default.agent01"
const shellInspect = "container inspect " + shellContainer
const shellExec = "exec -i --user=1000:1000 --env HOME=/home/agent --workdir=/workspace " + shellContainer + " /bin/bash"

// TestShellStreamsWithoutTerminal proves piped shell input, separate output streams, unprivileged identity and exit status survive RunWithHost and fake Podman.
func TestShellStreamsWithoutTerminal(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.responses["container exists "+shellContainer] = response{}
	f.responses[shellExec] = response{Output: "shell output\n", Stderr: "shell error\n", Exit: 23, EchoStdin: true}
	f.host.Stdin = strings.NewReader("echo from stdin\n")
	f.host.IsInputTerminal = func(io.Reader) bool { return false }
	f.host.IsOutputTerminal = func(io.Writer) bool { return false }
	data, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	var stdout, stderr bytes.Buffer
	status := cli.RunWithHost([]string{"shell", "agent01"}, &stdout, &stderr, f.host)
	if status != 23 || stdout.String() != "shell output\necho from stdin\n" || stderr.String() != "shell error\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q; want status=23 and unmodified shell streams", status, stdout.String(), stderr.String())
	}
	want := append(sandboxQueryCalls(), strings.Fields(shellExec))
	if calls := testutil.Invocations(t, f.fakes.PodmanLog); !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestShellDoesNotAllocateTerminalForFiles proves regular files, pipes, and the null device omit -t using the real terminal predicate through RunWithHost and fake Podman.
func TestShellDoesNotAllocateTerminalForFiles(t *testing.T) {
	for _, kind := range []string{"regular-file", "pipe", "null-device"} {
		t.Run(kind, func(t *testing.T) {
			f := linuxHost(t)
			configureSandboxQueries(f, "default", "")
			f.responses["container exists "+shellContainer] = response{}
			f.responses[shellExec] = response{Output: "shell output\n"}
			var input *os.File
			var err error
			switch kind {
			case "regular-file":
				input, err = os.CreateTemp(t.TempDir(), "input")
			case "pipe":
				var writer *os.File
				input, writer, err = os.Pipe()
				if err == nil {
					err = writer.Close()
				}
			case "null-device":
				input, err = os.Open(os.DevNull)
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { input.Close() })
			f.host.Stdin = input
			f.host.IsInputTerminal = nil
			status, output, calls := f.run(t, "shell", "agent01")
			if status != 0 || output != "shell output\n" {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			if call := calls[len(calls)-1]; !reflect.DeepEqual(call, strings.Fields(shellExec)) {
				t.Fatalf("exec=%v want=%v", call, strings.Fields(shellExec))
			}
		})
	}
}

// TestShellOmitsTerminalForPipedOutput proves terminal stdin with piped stdout omits -t through RunWithHost's terminal predicate and fake Podman.
func TestShellOmitsTerminalForPipedOutput(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.IsInputTerminal = func(io.Reader) bool { return true }
	f.host.IsOutputTerminal = func(output io.Writer) bool {
		if _, ok := output.(*bytes.Buffer); !ok {
			t.Fatal("output predicate did not receive piped stdout")
		}
		return false
	}
	f.responses[strings.Replace(shellExec, "exec -i ", "exec -i -t ", 1)] = response{Output: "shell output\n"}
	status, stdout, stderr, calls := runCommand(t, f, "shell", "agent01")
	if status != 0 || stdout != "shell output\n" || stderr != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	if call := calls[len(calls)-1]; !reflect.DeepEqual(call, strings.Fields(shellExec)) {
		t.Fatalf("exec=%v want=%v", call, strings.Fields(shellExec))
	}
}

// TestShellAllocatesTerminal proves terminal stdin and stdout pass both stdin and TTY flags through RunWithHost's terminal predicates and fake Podman.
func TestShellAllocatesTerminal(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.host.Stdin = strings.NewReader("")
	f.host.IsInputTerminal = func(input io.Reader) bool {
		if input != f.host.Stdin {
			t.Fatal("terminal predicate received different stdin")
		}
		return true
	}
	f.host.IsOutputTerminal = func(output io.Writer) bool {
		if _, ok := output.(*bytes.Buffer); !ok {
			t.Fatal("output predicate received different stdout")
		}
		return true
	}
	f.responses["container exists "+shellContainer] = response{}
	f.responses["exec -i -t --user=1000:1000 --env HOME=/home/agent --workdir=/workspace "+shellContainer+" /bin/bash"] = response{Output: "terminal shell\n"}
	status, output, calls := f.run(t, "shell", "agent01")
	if status != 0 || output != "terminal shell\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	want := []string{"exec", "-i", "-t", "--user=1000:1000", "--env", "HOME=/home/agent", "--workdir=/workspace", shellContainer, "/bin/bash"}
	if !reflect.DeepEqual(calls[len(calls)-1], want) {
		t.Fatalf("exec=%v want=%v", calls[len(calls)-1], want)
	}
	noMutation(t, calls)
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestShellRefusesUnknownSandbox proves an unknown sandbox is named and refuses inspection or exec through RunWithHost and fake Podman.
func TestShellRefusesUnknownSandbox(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.responses["container exists "+shellContainer] = response{Exit: 1}
	status, output, calls := f.run(t, "shell", "agent01")
	if status != 1 || output != "sandbox \"agent01\" does not exist in this controller group\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	want := sandboxExistenceCalls()
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestShellRefusesStoppedSandbox proves a stopped sandbox names start NAME and invokes no shell or lifecycle mutation through RunWithHost and fake Podman.
func TestShellRefusesStoppedSandbox(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.responses["container exists "+shellContainer] = response{}
	f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, "false")}
	status, output, calls := f.run(t, "shell", "agent01")
	if status != 1 || output != "sandbox \"agent01\" is stopped; run sandboxed-agents start agent01\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	want := sandboxQueryCalls()
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestShellRequiresName proves shell without NAME returns exact usage and calls no process through RunWithHost and fake Podman.
func TestShellRequiresName(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	status, output, calls := f.run(t, "shell")
	want := "step 1: sandbox name must follow the command path\nusage: sandboxed-agents COMMAND [ARG...]\n"
	if status != 2 || output != want {
		t.Fatalf("status=%d output=%q want=%q", status, output, want)
	}
	if len(calls) != 0 {
		t.Fatalf("unexpected Podman calls: %v", calls)
	}
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}

// TestShellReportsPodmanExecutionFailures proves Podman exit code 125 passes through with only Podman stderr through RunWithHost and fake Podman.
func TestShellReportsPodmanExecutionFailures(t *testing.T) {
	code := 125
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.host.Stdin = strings.NewReader("")
	f.responses["container exists "+shellContainer] = response{}
	f.responses[shellExec] = response{Stderr: "Podman execution failure\n", Exit: code}
	status, output, calls := f.run(t, "shell", "agent01")
	if status != code || output != "Podman execution failure\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	noMutation(t, calls)
}

// TestShellReportsLaunchFailure proves a process launch failure adds a CLI diagnostic through RunWithHost and fake Podman. It runs on Linux only.
func TestShellReportsLaunchFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test removes a running fake executable, which Windows does not allow; the launch branch is proven on Linux")
	}
	f := sandboxStateFixture(t)
	changed := changePodmanAfterSandboxQueries(t, f, func() error {
		return os.Remove(fakePath(f, "podman"))
	})
	status, stdout, stderr, calls := runCommand(t, f, "shell", "agent01")
	changed()
	if status != 1 || stdout != "" || !strings.Contains(stderr, "podman exec -i ") || !strings.Contains(stderr, "executable file not found") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout, stderr)
	}
	if !reflect.DeepEqual(calls, sandboxQueryCalls()) {
		t.Fatalf("calls=%v want=%v", calls, sandboxQueryCalls())
	}
}

// TestShellReportsSignalTermination proves a signal-terminated Podman returns status 1 and a CLI diagnostic through RunWithHost and a fake Podman executable.
func TestShellReportsSignalTermination(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("signal termination requires the Linux shell")
	}
	f := sandboxStateFixture(t)
	changed := changePodmanAfterSandboxQueries(t, f, func() error {
		path := fakePath(f, "podman")
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("#!/bin/sh\nkill -TERM $$\n"), 0700)
	})
	status, stdout, stderr, calls := runCommand(t, f, "shell", "agent01")
	changed()
	wantDiagnostic := "podman " + shellExec + ": terminated by a signal"
	if status != 1 || stdout != "" || !strings.Contains(stderr, wantDiagnostic) {
		t.Fatalf("status=%d stdout=%q stderr=%q; want status=1 and diagnostic containing %q", status, stdout, stderr, wantDiagnostic)
	}
	if !reflect.DeepEqual(calls, sandboxQueryCalls()) {
		t.Fatalf("calls=%v want=%v", calls, sandboxQueryCalls())
	}
}

// changePodmanAfterSandboxQueries changes the process boundary while the final
// sandbox query waits, so readiness succeeds before shell execution can fail.
func changePodmanAfterSandboxQueries(t *testing.T, f *fixture, change func() error) func() {
	t.Helper()
	dir := t.TempDir()
	ready, resume := filepath.Join(dir, "ready"), filepath.Join(dir, "resume")
	lastQuery := "container exists " + sandboxBackup
	answer := f.responses[lastQuery]
	answer.ReadyFile, answer.WaitFor = ready, resume
	f.responses[lastQuery] = answer
	done := make(chan error, 1)
	go func() {
		deadline := time.After(5 * time.Second)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-deadline:
				done <- fmt.Errorf("final sandbox query did not reach the process boundary")
				return
			case <-tick.C:
				if _, err := os.Stat(ready); err != nil {
					continue
				}
				err := change()
				if resumeErr := os.WriteFile(resume, nil, 0600); err == nil {
					err = resumeErr
				}
				done <- err
				return
			}
		}
	}()
	return func() {
		t.Helper()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// TestShellPassesThroughCommandFailureStatuses proves shell exits 23, 125, 126 and 127 preserve separate streams without CLI diagnostics through RunWithHost and fake Podman.
func TestShellPassesThroughCommandFailureStatuses(t *testing.T) {
	for _, code := range []int{23, 125, 126, 127} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := linuxHost(t)
			configureSandboxQueries(f, "default", "")
			f.host.Stdin = strings.NewReader("")
			f.responses["container exists "+shellContainer] = response{}
			f.responses[shellExec] = response{Output: "shell output\n", Stderr: "shell command failed\n", Exit: code}
			data, err := json.Marshal(f.responses)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_PODMAN_RESPONSES", string(data))
			var stdout, stderr bytes.Buffer
			status := cli.RunWithHost([]string{"shell", "agent01"}, &stdout, &stderr, f.host)
			if status != code || stdout.String() != "shell output\n" || stderr.String() != "shell command failed\n" {
				t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
			}
		})
	}
}

// TestShellRefusesUnverifiedSandboxState proves failed existence or inspection and malformed running state cannot launch a shell through RunWithHost and fake Podman.
func TestShellRefusesUnverifiedSandboxState(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     response
		message   string
		calls     int
	}{
		{"existence-failure", "container exists " + shellContainer, response{Stderr: "existence failed", Exit: 125}, "existence failed", 1},
		{"inspection-failure", shellInspect, response{Stderr: "inspection failed", Exit: 125}, "inspection failed", 2},
		{"malformed-state", shellInspect, response{Output: "unknown"}, "invalid inspect answer", 2},
		{"empty-state", shellInspect, response{}, "invalid inspect answer", 2},
		{"missing-running-state", shellInspect, response{Output: `[{"Config":{"Labels":{"io.sandboxed-agents.owner":"default"}},"State":{}}]`}, "running state is missing", 6},
		{"invalid-running-type", shellInspect, response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default"}`, `"unknown"`)}, "invalid inspect answer", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := linuxHost(t)
			configureSandboxQueries(f, "default", "")
			f.responses["container exists "+shellContainer] = response{}
			f.responses[tc.key] = tc.value
			status, output, calls := f.run(t, "shell", "agent01")
			if status != 1 || !strings.Contains(output, tc.message) || len(calls) != tc.calls {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
			for _, call := range calls {
				if call[0] == "exec" {
					t.Fatalf("unexpected shell exec: %v", call)
				}
			}
			noMutation(t, calls)
		})
	}
}

// TestShellSkipsLinuxPreflight proves shell reaches an existing sandbox with only Podman on PATH and no prerequisite queries through RunWithHost and fake Podman.
func TestShellSkipsLinuxPreflight(t *testing.T) {
	f := linuxHost(t)
	configureSandboxQueries(f, "default", "")
	f.responses["container exists "+shellContainer] = response{}
	f.responses[shellExec] = response{Output: "shell output\n"}
	f.host.Stdin = strings.NewReader("")
	f.host.ReadFile = func(path string) ([]byte, error) {
		t.Fatalf("unexpected prerequisite read: %s", path)
		return nil, os.ErrNotExist
	}
	f.host.CurrentUser = func() (*user.User, error) { t.Fatal("unexpected prerequisite user query"); return nil, os.ErrNotExist }
	podmanOnly := t.TempDir()
	data, err := os.ReadFile(fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(podmanOnly, filepath.Base(fakePath(f, "podman"))), data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", podmanOnly)
	status, output, calls := f.run(t, "shell", "agent01")
	if status != 0 || output != "shell output\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	want := append(sandboxQueryCalls(), strings.Fields(shellExec))
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

// TestWindowsShellUsesSelectedTarget proves sandbox queries and shell execution use the selected connection and remove ambient remote settings through RunWithHost and fake Podman.
func TestWindowsShellUsesSelectedTarget(t *testing.T) {
	f := windowsHost(t)
	configureSandboxQueries(f, "team", "--connection chosen ")
	t.Setenv("SANDBOXED_AGENTS_GROUP", "team")
	f.host.Stdin = strings.NewReader("")
	envLog := logAmbientPodmanEnvironment(t)
	const container = "sandboxed-agents.team.agent01"
	f.responses["--connection chosen container exists "+container] = response{}
	f.responses["--connection chosen exec -i --user=1000:1000 --env HOME=/home/agent --workdir=/workspace "+container+" /bin/bash"] = response{Output: "selected shell\n"}
	status, output, calls := f.run(t, "shell", "agent01")
	if status != 0 || output != "selected shell\n" {
		t.Fatalf("status=%d output=%q", status, output)
	}
	want := sandboxQueryCalls()
	for i, call := range want {
		for j, arg := range call {
			call[j] = strings.ReplaceAll(arg, ".default.", ".team.")
		}
		want[i] = append([]string{"--connection", "chosen"}, call...)
	}
	want = append(want, []string{"--connection", "chosen", "exec", "-i", "--user=1000:1000", "--env", "HOME=/home/agent", "--workdir=/workspace", container, "/bin/bash"})
	want = append([][]string{{"machine", "list", "--format", "json"}, {"machine", "inspect", "chosen"}}, want...)
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("sandbox calls=%v want=%v", calls, want)
	}
	assertNoAmbientPodmanEnvironment(t, envLog)
	noMutation(t, calls)
	if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
		t.Fatalf("unexpected SSH calls: %v", calls)
	}
}
