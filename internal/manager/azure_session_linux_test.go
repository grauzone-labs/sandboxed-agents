package manager_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/azurebrowser"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
	"golang.org/x/sys/unix"
)

// azureSessionOutput exposes startup at the manager output seam without racing terminal writes.
type azureSessionOutput struct{ ready chan struct{} }

// Write observes the private readiness record while discarding protocol output.
func (w azureSessionOutput) Write(data []byte) (int, error) {
	if strings.Contains(string(data), azurebrowser.SessionPrefix+"\"ready\"") {
		close(w.ready)
	}
	return len(data), nil
}

// TestAzureSessionCancelBeforeStartPreventsLateLogin proves cancel acknowledgement precludes a delayed start at the manager command and real socket seams.
func TestAzureSessionCancelBeforeStartPreventsLateLogin(t *testing.T) {
	if os.Getuid() != 1000 || os.Getgid() != 1000 {
		t.Skip("real socket peers require agent UID/GID 1000")
	}
	token := fmt.Sprintf("%x", randomAzureToken(t))
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	var diagnostics bytes.Buffer
	ready := make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- manager.RunWithEffects([]string{"__azure-login", token, "agent01"}, azureSessionOutput{ready}, &diagnostics, effects)
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("session did not initialize")
	}
	// The real socket verifies this test process's OS identity in addition to command identity.
	if status := manager.RunWithEffects([]string{"__azure-control", token, "cancel"}, io.Discard, io.Discard, effects); status != 0 {
		t.Fatal("cancel was not acknowledged")
	}
	select {
	case status := <-done:
		if status != 1 {
			t.Fatalf("cancel status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("manager survived cancellation ack")
	}
	if status := manager.RunWithEffects([]string{"__azure-control", token, "start"}, io.Discard, io.Discard, effects); status == 0 {
		t.Fatal("cancelled session accepted late start")
	}
}

// TestAzureSessionUnattendedStartupExpires proves a lost host never starts Azure and the waiting manager ends within its startup bound at the manager command seam.
func TestAzureSessionUnattendedStartupExpires(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	started := time.Now()
	var diagnostics bytes.Buffer
	if status := manager.RunWithEffects([]string{"__azure-login", fmt.Sprintf("%x", randomAzureToken(t)), "agent01"}, io.Discard, &diagnostics, effects); status != 1 || !strings.Contains(diagnostics.String(), "did not authorize startup") {
		t.Fatalf("status=%d diagnostics=%s", status, &diagnostics)
	}
	if elapsed := time.Since(started); elapsed < 9*time.Second || elapsed > 12*time.Second {
		t.Fatalf("startup timeout=%v", elapsed)
	}
}

// randomAzureToken returns a fresh capability without exposing it to test diagnostics.
func randomAzureToken(t *testing.T) []byte {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

// TestAzureSessionRejectsUnsafeControl proves arbitrary PID-like, malformed capability, and alias inputs cannot reach process control at the manager command seam.
func TestAzureSessionRejectsUnsafeControl(t *testing.T) {
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	for _, token := range []string{"", "1", "-1", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("a", 63) + "/"} {
		for _, verb := range []string{"__azure-login", "__azure-control"} {
			value := "cancel"
			if verb == "__azure-login" {
				value = "agent01"
			}
			if status := manager.RunWithEffects([]string{verb, token, value}, io.Discard, io.Discard, effects); status == 0 {
				t.Fatal("unsafe control accepted")
			}
		}
	}
	if status := manager.RunWithEffects([]string{"__azure-login", strings.Repeat("a", 64), "agent01;echo injected"}, io.Discard, io.Discard, effects); status == 0 {
		t.Fatal("unsafe alias accepted")
	}
	if status := manager.RunWithEffects([]string{"__azure-control", strings.Repeat("a", 64), "kill -1"}, io.Discard, io.Discard, effects); status == 0 {
		t.Fatal("arbitrary operation accepted")
	}
}

// azureControlChild exposes a real operating-system child to the manager's process seam.
type azureControlChild struct{ command *exec.Cmd }

// PID returns the owned child's PID for the manager's process interface.
func (c azureControlChild) PID() int { return c.command.Process.Pid }

// Wait reaps the real child after the manager ends it.
func (c azureControlChild) Wait() error { return c.command.Wait() }

// Signal signals only this test's owned child at the operating-system seam.
func (c azureControlChild) Signal(value os.Signal) error { return c.command.Process.Signal(value) }

// TestAzureSessionRejectsInvalidOperationsWithoutEndingLogin proves raw invalid requests and duplicate starts cannot end startup or an active child through manager commands and the real control socket.
func TestAzureSessionRejectsInvalidOperationsWithoutEndingLogin(t *testing.T) {
	if os.Getuid() != 1000 || os.Getgid() != 1000 {
		t.Skip("real socket peers require agent UID/GID 1000")
	}
	for _, phase := range []string{"startup", "active"} {
		t.Run(phase, func(t *testing.T) {
			token := fmt.Sprintf("%x", randomAzureToken(t))
			effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
			command := exec.Command("/bin/sleep", "30")
			started := make(chan struct{})
			effects.Spawn = func(process manager.Process) (manager.Child, error) {
				if !process.OwnProcessGroup {
					t.Error("login did not request owned process group")
				}
				err := command.Start()
				close(started)
				return azureControlChild{command}, err
			}
			ready := make(chan struct{})
			done := make(chan int, 1)
			go func() {
				done <- manager.RunWithEffects([]string{"__azure-login", token, "agent01"}, azureSessionOutput{ready}, io.Discard, effects)
			}()
			t.Cleanup(func() {
				if command.Process != nil {
					command.Process.Kill()
				}
				manager.RunWithEffects([]string{"__azure-control", token, "cancel"}, io.Discard, io.Discard, effects)
			})
			select {
			case <-ready:
			case <-time.After(time.Second):
				t.Fatal("session not ready")
			}
			if phase == "startup" {
				rejectAzureRawControl(t, token, "bogus\n")
			}
			if status := manager.RunWithEffects([]string{"__azure-control", token, "start"}, io.Discard, io.Discard, effects); status != 0 {
				t.Fatal("invalid request ended startup gate")
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("login did not start")
			}
			for _, operation := range []string{"bogus\n", "start\n", "\n", "cancels\n"} {
				rejectAzureRawControl(t, token, operation)
				if err := command.Process.Signal(syscall.Signal(0)); err != nil {
					t.Fatal("invalid control ended active login")
				}
				select {
				case <-done:
					t.Fatal("invalid control ended manager")
				default:
				}
			}
			if status := manager.RunWithEffects([]string{"__azure-control", token, "cancel"}, io.Discard, io.Discard, effects); status != 0 {
				t.Fatal("valid cancel not acknowledged")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancel did not finish manager")
			}
			if command.ProcessState == nil {
				t.Fatal("cancel acknowledgement preceded child wait")
			}
		})
	}
}

// rejectAzureRawControl sends an untrusted operation and requires rejection without a successful acknowledgement.
func rejectAzureRawControl(t *testing.T, token, operation string) {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Net: "unix", Name: "@sandboxed-agents-azure-" + token})
	if err != nil {
		t.Fatal("control socket unavailable")
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(connection, operation); err != nil {
		t.Fatal("cannot send raw control")
	}
	answer, err := io.ReadAll(connection)
	if err != nil || len(answer) != 0 {
		t.Fatal("invalid control was not rejected")
	}
}

// TestAzureSessionReadsInteractiveTerminal proves owned login and account-check groups can both read the real controlling terminal through the manager executable seam.
func TestAzureSessionReadsInteractiveTerminal(t *testing.T) {
	if os.Getuid() != 1000 || os.Getgid() != 1000 {
		t.Skip("production manager requires agent UID/GID 1000")
	}
	token := fmt.Sprintf("%x", randomAzureToken(t))
	directory := t.TempDir()
	// Azure is the external program boundary; both steps deliberately require terminal input.
	script := "#!/bin/sh\nIFS= read -r answer || exit 3\n[ \"$answer\" = expected ] || exit 4\n"
	if err := os.WriteFile(filepath.Join(directory, "az"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	master, slave := openTerminal(t, 24, 80)
	defer slave.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestAzureSessionTerminalBoundary$")
	command.Env = append(os.Environ(), "TEST_AZURE_TTY_TOKEN="+token, "PATH="+directory)
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	effects := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	t.Cleanup(func() {
		manager.RunWithEffects([]string{"__azure-control", token, "cancel"}, io.Discard, io.Discard, effects)
		command.Process.Kill()
	})
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(master)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), azurebrowser.SessionPrefix+"\"ready\"") {
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("terminal manager did not initialize")
	}
	if status := manager.RunWithEffects([]string{"__azure-control", token, "start"}, io.Discard, io.Discard, effects); status != 0 {
		t.Fatal("terminal manager did not start")
	}
	if _, err := io.WriteString(master, "expected\nexpected\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("terminal workflow failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned Azure process group could not read interactive terminal")
	}
}

// TestAzureSessionTerminalBoundary proves the production manager restores its foreground process group through the executable and real controlling-terminal seam.
func TestAzureSessionTerminalBoundary(t *testing.T) {
	token := os.Getenv("TEST_AZURE_TTY_TOKEN")
	if token == "" {
		return
	}
	status := manager.Run([]string{"__azure-login", token, "agent01"}, os.Stdout, os.Stderr)
	foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		os.Exit(42)
	}
	os.Exit(status)
}
