package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/azurebrowser"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestAzureBrowserRequiresSSHSetup proves missing host setup refuses before any login or terminal check through the host CLI seam.
func TestAzureBrowserRequiresSSHSetup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := sandboxStateFixture(t)
	f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}`, "true")}
	f.responses[integrationProbe] = response{}
	f.host.IsInputTerminal = func(io.Reader) bool { t.Error("terminal checked before SSH setup"); return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	want := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
	if status != 1 || !strings.Contains(output, "ssh-config agent01 --install") || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
	}
}

// TestAzureLoginRequiresToolchainBeforeTerminal proves Azure names the complete additive update command before checking terminal or launching login through the host CLI seam.
func TestAzureLoginRequiresToolchainBeforeTerminal(t *testing.T) {
	for _, tc := range []struct{ set, want string }{{"none", "update agent01 --with azure"}, {"dotnet,native", "update agent01 --with azure,dotnet,native"}} {
		f := sandboxStateFixture(t)
		f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"`+tc.set+`"}`, "true")}
		f.responses[integrationProbe] = response{}
		f.host.IsInputTerminal = func(io.Reader) bool { t.Error("terminal checked before toolchain"); return false }
		status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", "device-code")
		if status != 1 || !strings.Contains(output, tc.want) || !reflect.DeepEqual(calls, append(sandboxQueryCalls(), strings.Fields(integrationProbe))) {
			t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
		}
	}
}

// TestAzureLoginDispatchesAgentWithTerminal proves both Azure workflows preserve terminal streams, identity, and failure normalization through the host CLI seam.
func TestAzureLoginDispatchesAgentWithTerminal(t *testing.T) {
	for _, workflow := range []string{"device-code", "browser"} {
		for _, code := range []int{0, 23} {
			t.Run(workflow+strconv.Itoa(code), func(t *testing.T) {
				f := sandboxStateFixture(t)
				state := t.TempDir()
				t.Setenv("XDG_STATE_HOME", state)
				t.Setenv("LOCALAPPDATA", state)
				t.Setenv("AZURE_CONFIG_DIR", "/host/azure")
				f.responses[shellInspect] = response{Output: containerInspectAnswer(`{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.toolchains":"azure"}`, "true")}
				f.responses[integrationProbe] = response{}
				f.host.IsInputTerminal = func(io.Reader) bool { return true }
				f.host.IsOutputTerminal = func(io.Writer) bool { return true }
				f.host.Stdin = strings.NewReader("azure-terminal-input\n")
				execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -it " + shellContainer + " " + integrationManager + " integrations login azure " + workflow)
				if workflow == "browser" {
					hash := sha256.Sum256([]byte("agent01"))
					dir := filepath.Join(state, "sandboxed-agents", "group-default", "ssh", fmt.Sprintf("sandbox-%x", hash))
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("Host agent01\n"), 0600); err != nil {
						t.Fatal(err)
					}
					execution = strings.Fields("exec -it --user=1000:1000 --env HOME=/home/agent " + shellContainer + " " + integrationManager + " __azure-login * agent01")
					f.responses["exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control * start"] = response{}
					f.responses["exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control * cancel"] = response{}
				}
				answer := response{Exit: code, EchoStdin: true}
				if workflow == "browser" {
					answer.Output = "\x1eSANDBOXED_AGENTS_AZURE_SESSION \"ready\"\n\x1eSANDBOXED_AGENTS_AZURE_SESSION \"done\"\n"
				}
				f.responses[strings.Join(execution, " ")] = answer
				status, output, calls := f.run(t, "integrations", "login", "agent01", "azure", workflow)
				wantStatus := 0
				if code != 0 {
					wantStatus = 1
				}
				expected := append(sandboxQueryCalls(), strings.Fields(integrationProbe))
				if workflow == "browser" {
					token := calls[len(expected)][len(execution)-2]
					if !azurebrowser.ValidToken(token) {
						t.Fatalf("invalid session token")
					}
					execution[len(execution)-2] = token
					expected = append(expected, execution, strings.Fields("exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control "+token+" start"))
				} else {
					expected = append(expected, execution)
				}
				if status != wantStatus || !strings.Contains(output, "azure-terminal-input") || !reflect.DeepEqual(calls, expected) {
					t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
				}
			})
		}
	}
}

// azureBrowserFixture supplies installed SSH setup and an interactive Azure sandbox at the host CLI seam.
func azureBrowserFixture(t *testing.T, login response) *fixture {
	t.Helper()
	f := azureIntegrationFixture(t)
	f.responses[integrationProbe] = response{}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("LOCALAPPDATA", state)
	hash := sha256.Sum256([]byte("agent01"))
	dir := filepath.Join(state, "sandboxed-agents", "group-default", "ssh", fmt.Sprintf("sandbox-%x", hash))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("Host agent01\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.host.IsInputTerminal = func(io.Reader) bool { return true }
	f.host.IsOutputTerminal = func(io.Writer) bool { return true }
	login.Output = "\x1eSANDBOXED_AGENTS_AZURE_SESSION \"ready\"\n" + login.Output
	login.FinalOutput = "\x1eSANDBOXED_AGENTS_AZURE_SESSION \"done\"\n"
	f.responses["exec -it --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-login * agent01"] = login
	f.responses["exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control * start"] = response{}
	f.responses["exec --user=1000:1000 --env HOME=/home/agent "+shellContainer+" "+integrationManager+" __azure-control * cancel"] = response{}
	return f
}

// TestAzureBrowserOpensTunnel proves the login command opens loopback forwarding for the actual callback before displaying its URL and reaps SSH on completion through real process boundaries.
func TestAzureBrowserOpensTunnel(t *testing.T) {
	for _, exit := range []int{0, 23} {
		t.Run(strconv.Itoa(exit), func(t *testing.T) {
			address := "https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A43123&state=example"
			f := azureBrowserFixture(t, response{Output: "login prompt\n\x1eSANDBOXED_AGENTS_AZURE_BROWSER " + strconv.Quote(address) + "\naccount verified\n", DelayAfterOutputMS: 100, Exit: exit})
			pidFile := filepath.Join(t.TempDir(), "ssh.pid")
			f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", EchoStdin: true, PIDFile: pidFile}}
			status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
			calls := f.sshCalls(t)
			assertAzureSSHExited(t, pidFile)
			wantStatus := 0
			if exit != 0 {
				wantStatus = 1
			}
			if status != wantStatus || len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-L 127.0.0.1:43123:127.0.0.1:43123") || !strings.Contains(output, address) || strings.Contains(output, "SANDBOXED_AGENTS_AZURE_BROWSER") {
				t.Fatalf("status=%d output=%q ssh=%v", status, output, calls)
			}
		})
	}
}

// TestAzureBrowserRefusesBrokenForwarding proves tunnel failure prevents URL display and normalizes failure at the host CLI seam.
func TestAzureBrowserRefusesBrokenForwarding(t *testing.T) {
	for _, answer := range []response{{Exit: 255}, {Output: "wrong readiness marker\n"}} {
		f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n", DelayAfterOutputMS: 500})
		f.sshResponses = map[string]response{"*": answer}
		status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
		if status != 1 || !strings.Contains(output, "SSH tunnel could not establish") || strings.Contains(output, "http://localhost") {
			t.Fatalf("status=%d output=%q", status, output)
		}
	}
}

// TestAzureBrowserRejectsUntrustedRecords proves malformed, oversized, and nonloopback callback records cannot start SSH through the host CLI seam.
func TestAzureBrowserRejectsUntrustedRecords(t *testing.T) {
	for _, record := range []string{"not-json\n", "\"http://example.com:43123\"\n", "\"http://localhost:0\"\n", "\"http://localhost:43123\"", "\"" + strings.Repeat("x", 65537) + "\"\n"} {
		f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER " + record, DelayAfterOutputMS: 50})
		status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
		if status != 1 || len(f.sshCalls(t)) != 0 {
			t.Fatalf("status=%d output=%q ssh=%v", status, output, f.sshCalls(t))
		}
	}
}

// TestAzureBrowserDetectsTunnelExit proves a lost SSH connection cancels an ongoing login through the host CLI and child-process seam.
func TestAzureBrowserDetectsTunnelExit(t *testing.T) {
	f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n", DelayAfterOutputMS: 10000})
	f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", DelayAfterOutputMS: 50, Exit: 255}}
	status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if status != 1 || !strings.Contains(output, "SSH tunnel ended before login completed") {
		t.Fatalf("status=%d output=%q", status, output)
	}
}

// assertAzureSSHExited verifies the observed child process was reaped on Linux rather than left forwarding.
func assertAzureSSHExited(t *testing.T, pidFile string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	defer process.Release()
	if process.Signal(syscall.Signal(0)) == nil {
		t.Fatalf("SSH process %d still running", pid)
	}
}

// TestAzureBrowserCancellationReapsTunnel proves Ctrl+C reports cancellation and reaps SSH during startup and after readiness through the executable CLI seam.
func TestAzureBrowserCancellationReapsTunnel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("subprocess interrupt assertion requires Unix signals")
	}
	for _, phase := range []string{"startup", "ready"} {
		t.Run(phase, func(t *testing.T) {
			f := azureBrowserFixture(t, response{Output: "\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n", DelayAfterOutputMS: 10000})
			dir := t.TempDir()
			pidFile, ready := filepath.Join(dir, "ssh.pid"), filepath.Join(dir, "ready")
			sshOutput := ""
			if phase == "ready" {
				sshOutput = "sandboxed-agents-azure-ready\n"
			}
			f.sshResponses = map[string]response{"*": {Output: sshOutput, EchoStdin: true, PIDFile: pidFile, ReadyFile: ready}}
			podmanData, _ := json.Marshal(f.responses)
			sshData, _ := json.Marshal(f.sshResponses)
			t.Setenv("TEST_PODMAN_RESPONSES", string(podmanData))
			t.Setenv("TEST_SSH_RESPONSES", string(sshData))
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
				_, readyErr := os.Stat(ready)
				data, _ := os.ReadFile(outputPath)
				if readyErr == nil && (phase == "startup" || strings.Contains(string(data), "SSH tunnel ready")) {
					break
				}
				if time.Now().After(deadline) {
					command.Process.Kill()
					<-done
					t.Fatalf("SSH did not reach %s: %s", phase, data)
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
					t.Fatal("cancelled login succeeded")
				}
			case <-time.After(5 * time.Second):
				command.Process.Kill()
				<-done
				t.Fatal("cancelled login did not exit")
			}
			data, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "Azure browser login cancelled") || strings.Contains(string(data), "SSH tunnel ended") || strings.Contains(string(data), "could not establish forwarding") {
				t.Fatalf("wrong cancellation diagnostic: %s", data)
			}
			assertAzureSSHExited(t, pidFile)
		})
	}
}

// TestAzureBrowserExecutableBoundary drives an interactive browser login in a child process for cancellation checks.
func TestAzureBrowserExecutableBoundary(t *testing.T) {
	if os.Getenv("TEST_AZURE_BROWSER_EXECUTABLE") != "1" {
		return
	}
	host := cli.Host{IsInputTerminal: func(io.Reader) bool { return true }, IsOutputTerminal: func(io.Writer) bool { return true }}
	os.Exit(cli.RunWithHost([]string{"integrations", "login", "agent01", "azure", "browser"}, os.Stdout, os.Stderr, host))
}

// TestAzureBrowserReplacesCallbackTunnel proves repeated callbacks replace forwarding without cancelling login through the host CLI seam.
func TestAzureBrowserReplacesCallbackTunnel(t *testing.T) {
	record := "\x1eSANDBOXED_AGENTS_AZURE_BROWSER \"http://localhost:43123\"\n"
	f := azureBrowserFixture(t, response{Output: record + record, DelayAfterOutputMS: 100})
	f.sshResponses = map[string]response{"*": {Output: "sandboxed-agents-azure-ready\n", EchoStdin: true}}
	status, output, _ := f.run(t, "integrations", "login", "agent01", "azure", "browser")
	if status != 0 || len(f.sshCalls(t)) != 2 {
		t.Fatalf("status=%d output=%q ssh=%v", status, output, f.sshCalls(t))
	}
}

// azureFailOnceWriter fails the first diagnostic write and captures the CLI's final failure message.
type azureFailOnceWriter struct {
	bytes.Buffer
	failed bool
}

// Write rejects one diagnostic before accepting the terminal workflow error.
func (w *azureFailOnceWriter) Write(data []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.Buffer.Write(data)
}

// TestAzureBrowserOutputFailureIsNotCancellation proves a failed diagnostic stream reports workflow failure rather than user cancellation through the host CLI seam.
func TestAzureBrowserOutputFailureIsNotCancellation(t *testing.T) {
	f := azureBrowserFixture(t, response{Stderr: "login diagnostic\n", DelayAfterOutputMS: 10000})
	data, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	var diagnostics azureFailOnceWriter
	status := cli.RunWithHost([]string{"integrations", "login", "agent01", "azure", "browser"}, io.Discard, &diagnostics, f.host)
	if status != 1 || !strings.Contains(diagnostics.String(), "integration workflow failed") || strings.Contains(diagnostics.String(), "login cancelled") {
		t.Fatalf("status=%d output=%q", status, diagnostics.String())
	}
}
