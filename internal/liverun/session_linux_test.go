package liverun_test

import (
	"bytes"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRunSurvivesLaunchingSessionHangup proves the owned supervisor escapes the launching session and its SIGHUP.
func TestRunSurvivesLaunchingSessionHangup(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	binary, _ := os.Executable()
	launcher := exec.Command(binary, "_fixture-session", private)
	launcher.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var output bytes.Buffer
	launcher.Stdout, launcher.Stderr = &output, &output
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	defer launcher.Process.Kill()
	waitFixture(t, filepath.Join(root, "session-ready"))
	var active struct{ Supervisor struct{ PID int } }
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "processes.json")), &active); err != nil {
		t.Fatal(err)
	}
	sid, err := unix.Getsid(active.Supervisor.PID)
	if err != nil || sid == launcher.Process.Pid || sid != active.Supervisor.PID {
		t.Fatalf("supervisor did not acquire its own session: sid=%d launcher=%d supervisor=%d err=%v", sid, launcher.Process.Pid, active.Supervisor.PID, err)
	}
	if err = syscall.Kill(-launcher.Process.Pid, syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	if err = launcher.Wait(); err == nil {
		t.Fatal("fixture session survived hangup")
	}
	if err = os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct {
		Code        int
		Interrupted bool
	}
	if err = json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 || ended.Interrupted {
		t.Fatalf("hangup changed detached suite: %+v", ended)
	}
}
