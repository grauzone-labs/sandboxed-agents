// Package liverun owns the host-side phases of one explicitly authorized live validation run.
package liverun

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

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
	return os.Rename(name, path)
}
