// Package liverun owns the host-side phases of one explicitly authorized live validation run.
package liverun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// Invocation describes one external process at the host boundary.
type Invocation struct {
	Program string
	Args    []string
	Dir     string
	Env     []string
	Input   []byte
}

// Executor executes queries and the durations collector without exposing their output.
type Executor interface {
	// Execute runs one invocation and returns its captured output. It can return
	// that output together with an error: the collector reads both to tell a
	// failed command from case durations that were retained while optional
	// operation evidence failed.
	Execute(context.Context, Invocation) ([]byte, error)
}

// DiskReader reads available bytes without requiring administrator privileges.
type DiskReader func(string) (uint64, error)

// Service supplies host boundaries to the repository-owned phase commands.
type Service struct {
	Executor    Executor
	FreeSpace   DiskReader
	Platform    string
	MachineDisk func(string) (string, error)
	// SessionProbe injects process-job observations at the host boundary.
	SessionProbe func() (SessionPolicy, error)
	// StartSupervisor injects the host process-start boundary for deterministic launch-failure tests.
	StartSupervisor func(*exec.Cmd) error
	// CoordinationRoot injects an isolated host namespace in offline fixtures; production uses the user cache.
	CoordinationRoot string
	// BuildEvidence injects a trusted producer at the host boundary for offline policy tests.
	BuildEvidence func(context.Context, string) ([]BuildResult, error)
	// SuiteDeadline bounds the whole suite process, build included; zero selects the production default.
	SuiteDeadline time.Duration
}

// New returns the production host boundaries for the current operating system.
func New() *Service {
	return &Service{Executor: commandExecutor{}, FreeSpace: freeSpace, Platform: runtime.GOOS, MachineDisk: machineDisk}
}

// commandExecutor executes bounded host queries with Windows-compatible captured handles.
type commandExecutor struct{}

// Execute runs one process with a bounded lifetime and captures both output streams privately.
func (commandExecutor) Execute(ctx context.Context, in Invocation) ([]byte, error) {
	cmd := exec.Command(in.Program, in.Args...)
	cmd.Dir, cmd.Env = in.Dir, in.Env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	if in.Input != nil {
		cmd.Stdin = bytesReader(in.Input)
	}
	var out synchronizedBuffer
	err := capture.RunOwned(ctx, cmd, &out, &out)
	return out.Bytes(), err
}

// readJSON reads one private run artifact.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSON publishes a private artifact atomically beside its destination.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publish-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return retryReplace(func() error { return os.Rename(name, path) }, replaceBusy, replaceRetryWindow)
}

// replaceRetryWindow bounds how long publication waits for readers that hold the destination open.
const replaceRetryWindow = 500 * time.Millisecond

// retryReplace runs replace until it succeeds, fails with an error that busy
// does not classify as a transient open handle, or the window has passed, and
// returns the last error.
func retryReplace(replace func() error, busy func(error) bool, window time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		err := replace()
		if err == nil || !busy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
