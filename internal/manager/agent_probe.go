package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"syscall"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// agentProbeTimeout leaves room for worker startup and transport within the host's three-second manager deadline.
const agentProbeTimeout = time.Second

// probeOutput keeps a bounded, private copy of authentication output.
type probeOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

// Write discards output beyond 64 KiB while recording that the result is unusable.
func (b *probeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 64*1024 - b.buffer.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

// probeAgentStatus runs a managed command as agent, bounds its lifetime and pipe drain, and returns only an unambiguous authentication state.
func probeAgentStatus(command string, probe *catalog.Probe, effects Effects) string {
	deadline := time.Now().Add(agentProbeTimeout)
	if effects.Spawn == nil {
		return "unknown"
	}
	var output probeOutput
	child, err := effects.Spawn(Process{Program: command, Args: probe.Args, Identity: agentIdentity, ProcessGroup: true, Stdout: &output, Stderr: io.Discard})
	if err != nil {
		return "unknown"
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		_ = child.Signal(syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
		return "unknown"
	}
	probeError := err
	if output.overflow || !time.Now().Before(deadline) {
		return "unknown"
	}
	fields := make(map[string]any)
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "unknown"
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "unknown"
		}
		key, ok := token.(string)
		if !ok {
			return "unknown"
		}
		if _, duplicate := fields[key]; duplicate {
			return "unknown"
		}
		var value any
		if decoder.Decode(&value) != nil {
			return "unknown"
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return "unknown"
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "unknown"
	}
	signedIn, valid := fields[probe.BooleanField].(bool)
	if !valid {
		return "unknown"
	}
	if !signedIn && probe.SignedOutExitCode != 0 && probeError == nil {
		return "unknown"
	}
	if probeError != nil {
		var exit interface{ ExitCode() int }
		if signedIn || probe.SignedOutExitCode == 0 || !errors.As(probeError, &exit) || exit.ExitCode() != probe.SignedOutExitCode {
			return "unknown"
		}
	}
	if signedIn {
		return "yes"
	}
	return "no"
}
