package cli_test

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestAzureBrowserFailureEndsRemoteLogin proves failed forwarding ends a real remote manager and callback child while unrelated work survives at the host CLI and manager runtime seams.
func TestAzureBrowserFailureEndsRemoteLogin(t *testing.T) {
	for _, phase := range []string{"forwarding", "lost-tunnel", "output"} {
		t.Run(phase, func(t *testing.T) {
			f, dir := azureRemoteFixture(t)
			f.sshResponses = map[string]response{"*": {Exit: 255}}
			if phase != "forwarding" {
				f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", EchoStdin: true}}
			}
			if phase == "lost-tunnel" {
				f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", DelayAfterOutputMS: 50, Exit: 255}}
			}
			status := 0
			if phase == "output" {
				data, _ := json.Marshal(f.responses)
				t.Setenv("TEST_PODMAN_RESPONSES", string(data))
				data, _ = json.Marshal(f.sshResponses)
				t.Setenv("TEST_SSH_RESPONSES", string(data))
				var diagnostics bytes.Buffer
				status = cli.RunWithHost([]string{"integrations", "login", "agent01", "azure", "browser"}, &azureFailOnceWriter{}, &diagnostics, f.host)
				if strings.Contains(diagnostics.String(), "cleanup failed") {
					t.Fatal("remote cleanup was not acknowledged")
				}
			} else {
				var output string
				status, output, _ = f.run(t, "integrations", "login", "agent01", "azure", "browser")
				if strings.Contains(output, "cleanup failed") {
					t.Fatal("remote cleanup was not acknowledged")
				}
				if phase == "forwarding" && !strings.Contains(output, "could not establish forwarding") {
					t.Fatal("missing forwarding diagnostic")
				}
			}
			if status != 1 {
				t.Fatalf("aborted workflow status=%d", status)
			}
			assertAzureRemoteEnded(t, dir, false)
		})
	}
}

// azureRemoteFixture starts real manager and Azure child processes behind only the external Podman seam.
func azureRemoteFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	if os.Getuid() != 1000 || os.Getgid() != 1000 {
		t.Skip("production manager requires UID/GID 1000")
	}
	f := azureBrowserFixture(t, response{})
	dir := t.TempDir()
	t.Setenv("TEST_AZURE_REMOTE_DIR", dir)
	t.Setenv("TEST_AZURE_REMOTE_MANAGER", fakeBinary)
	data, err := os.ReadFile(fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.fakes.Dir, "az"), data, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unrelated.Close() })
	t.Cleanup(func() {
		for _, name := range []string{"az.pid", "listener.pid", "manager.pid"} {
			data, _ := os.ReadFile(filepath.Join(dir, name))
			pid, _ := strconv.Atoi(string(data))
			if pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	unrelatedProcess := exec.Command("/bin/sleep", "30")
	if err := unrelatedProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unrelatedProcess.Process.Kill(); unrelatedProcess.Wait() })
	t.Cleanup(func() {
		if err := unrelatedProcess.Process.Signal(syscall.Signal(0)); err != nil {
			t.Error("unrelated process was stopped")
		}
	})
	t.Cleanup(func() {
		connection, err := net.DialTimeout("tcp", unrelated.Addr().String(), time.Second)
		if err != nil {
			t.Error("unrelated listener was stopped")
		} else {
			connection.Close()
		}
	})
	return f, dir
}

// assertAzureRemoteEnded proves the manager, login child, and callback descendant ended without checking a stale account after failure.
func assertAzureRemoteEnded(t *testing.T, dir string, success bool) {
	t.Helper()
	address, err := os.ReadFile(filepath.Join(dir, "callback"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		connection, err := net.DialTimeout("tcp", string(address), 50*time.Millisecond)
		if err != nil {
			break
		}
		connection.Close()
		if time.Now().After(deadline) {
			t.Fatal("remote Azure login callback listener survived failed SSH forwarding")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "account")); (err == nil) != success {
		t.Fatalf("account verification performed=%v want %v", err == nil, success)
	}
	for _, name := range []string{"az.pid", "listener.pid", "manager.pid"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
			if os.IsNotExist(err) || (err == nil && strings.Contains(string(stat), ") Z ")) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("owned %s process still running", name)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestAzureBrowserSuccessEndsRemoteDescendants proves successful login checks the account and cleans lingering descendants at the host CLI and manager runtime seams.
func TestAzureBrowserSuccessEndsRemoteDescendants(t *testing.T) {
	f, dir := azureRemoteFixture(t)
	t.Setenv("TEST_AZURE_REMOTE_MODE", "success")
	f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", EchoStdin: true}}
	status, _, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if status != 0 {
		t.Fatalf("successful workflow status=%d", status)
	}
	assertAzureRemoteEnded(t, dir, true)
}

// TestAzureBrowserMissingManagerCompletionReportsCleanupFailure proves unavailable remote cleanup cannot silently succeed at the host CLI seam.
func TestAzureBrowserMissingManagerCompletionReportsCleanupFailure(t *testing.T) {
	f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n", DelayAfterOutputMS: 10000})
	f.responses["exec --user=1000:1000 --env HOME=/home/agent -it "+shellContainer+" "+integrationManager+" __azure-login * agent01"] = response{Output: "\x1eSANDBOXED_AGENTS_AZURE_SESSION \"ready\"\n\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n", DelayAfterOutputMS: 10000}
	f.responses["exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control * cancel"] = response{Exit: 1}
	f.sshResponses = map[string]response{"*": {Exit: 255}}
	status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if status != 1 || !strings.Contains(output, "remote cleanup failed") {
		t.Fatal("missing truthful cleanup failure")
	}
}

// TestAzureBrowserOldManagerRequiresUpdate proves an incompatible manager refuses before any sign-in through the host CLI seam.
func TestAzureBrowserOldManagerRequiresUpdate(t *testing.T) {
	f := azureBrowserFixture(t, response{})
	f.responses["exec --user=1000:1000 --env HOME=/home/agent -it "+shellContainer+" "+integrationManager+" __azure-login * agent01"] = response{Exit: 2}
	status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if status != 1 || !strings.Contains(output, "update agent01") || len(calls) != len(sandboxQueryCalls())+2 {
		t.Fatal("incompatible manager did not refuse with update guidance")
	}
}

// TestAzureBrowserInterruptEndsRemoteLogin proves interrupts during SSH startup and ready forwarding terminate the real remote process group at the executable CLI seam.
func TestAzureBrowserInterruptEndsRemoteLogin(t *testing.T) {
	for _, phase := range []string{"startup", "ready"} {
		t.Run(phase, func(t *testing.T) {
			f, dir := azureRemoteFixture(t)
			marker := ""
			if phase == "ready" {
				marker = "sandboxed-agents-azure-ready\n"
			}
			sshReady := filepath.Join(dir, "ssh-ready")
			f.sshResponses = map[string]response{"*": {Output: marker, EchoStdin: true, ReadyFile: sshReady}}
			data, _ := json.Marshal(f.responses)
			t.Setenv("TEST_PODMAN_RESPONSES", string(data))
			data, _ = json.Marshal(f.sshResponses)
			t.Setenv("TEST_SSH_RESPONSES", string(data))
			t.Setenv("TEST_UP_EXECUTABLE", "1")
			t.Setenv("TEST_AZURE_BROWSER_EXECUTABLE", "1")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(dir, "output")
			output, err := os.Create(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			command := exec.Command(executable, "-test.run=^TestAzureBrowserExecutableBoundary$")
			command.Stdout, command.Stderr = output, output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				_, readyErr := os.Stat(sshReady)
				data, _ := os.ReadFile(outputPath)
				if readyErr == nil && (phase == "startup" || strings.Contains(string(data), "SSH tunnel ready")) {
					break
				}
				if time.Now().After(deadline) {
					command.Process.Kill()
					<-done
					t.Fatal("SSH did not reach interrupt phase")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := command.Process.Signal(os.Interrupt); err != nil {
				command.Process.Kill()
				<-done
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled workflow succeeded")
				}
			case <-time.After(5 * time.Second):
				command.Process.Kill()
				<-done
				t.Fatal("interrupted workflow did not return")
			}
			data, err = os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "Azure browser login cancelled") || strings.Contains(string(data), "cleanup failed") {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, "Azure browser") && !strings.Contains(line, "://") {
						t.Log(line)
					}
				}
				t.Fatal("interrupt did not report clean cancellation")
			}
			assertAzureRemoteEnded(t, dir, false)
		})
	}
}
