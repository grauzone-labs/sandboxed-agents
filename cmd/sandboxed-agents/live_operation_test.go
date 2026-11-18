package main_test

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// liveTimings owns sanitized measurements; copied hosts retain their case attribution.
type liveTimings struct {
	sync.Mutex
	evidence validationduration.Operations
}

// recordOperation appends one process result, discarding all command details.
func (host liveHost) recordOperation(source, operation string, started time.Time, err error) {
	if host.timings == nil {
		return
	}
	host.timings.Lock()
	defer host.timings.Unlock()
	result := "pass"
	if err != nil {
		result = "fail"
	}
	host.timings.evidence.Operations = append(host.timings.evidence.Operations, validationduration.Operation{Case: host.caseName, Source: source, Operation: operation, Result: result, Seconds: time.Since(started).Seconds()})
}

// liveCLIClass assigns only fixed operation names; no argument is retained.
func liveCLIClass(args []string) string {
	if len(args) == 0 {
		return "inspection"
	}
	switch args[0] {
	case "check", "version":
		if len(args) == 1 {
			return "preflight"
		}
		return "inspection"
	case "up":
		// up includes any required build and readiness; this timing never splits it.
		return "image build"
	case "stop", "start", "restart":
		return "sandbox start or restart"
	case "shell":
		return "smoke"
	case "update":
		return "update or rollback"
	case "build":
		return "rebuild"
	case "remove":
		return "cleanup"
	case "agents":
		if len(args) > 1 && args[1] == "enable" {
			return "agent install"
		}
		return "agent session"
	case "tools":
		if len(args) > 1 {
			switch args[1] {
			case "enable":
				return "Tool install"
			case "update":
				return "Tool update"
			case "delete":
				return "Tool delete"
			}
		}
		return "Tool probe"
	}
	return "inspection"
}

// livePodmanClass labels direct checks without retaining object identities or probes.
func (host liveHost) livePodmanClass(args []string) string {
	if host.caseName == "t3code" {
		return "Tool probe"
	}
	if slices.Contains(args, "ssh-host-key") || slices.Contains(args, "cat /proc/1/comm; cat /proc/1/status") {
		return "SSH server check"
	}
	return "inspection"
}

// timedLiveTerminal adds a single timing to a real terminal without changing its lifetime.
type timedLiveTerminal struct {
	*liveTerminal
	host      liveHost
	started   time.Time
	operation string
	once      sync.Once
}

// startTerminal measures terminal CLI startup failures as well as successful invocations.
func (host liveHost) startTerminal(t *testing.T, args ...string) *timedLiveTerminal {
	t.Helper()
	started := time.Now()
	ready := false
	defer func() {
		if !ready {
			host.recordOperation("cli", liveCLIClass(args), started, fmt.Errorf("terminal startup failed"))
		}
	}()
	terminal := &timedLiveTerminal{liveTerminal: startLiveTerminal(t, host.environment, host.binary, args...), host: host, started: started, operation: liveCLIClass(args)}
	ready = true
	return terminal
}

// Wait records the first completed invocation after its terminal output drains.
func (terminal *timedLiveTerminal) Wait(timeout time.Duration) (int, bool) {
	exit, finished := terminal.liveTerminal.Wait(timeout)
	if finished {
		terminal.finish(exit)
	}
	return exit, finished
}

// finish records one result even when the caller waits repeatedly and then closes.
func (terminal *timedLiveTerminal) finish(exit int) {
	terminal.once.Do(func() {
		var err error
		if exit != 0 {
			err = fmt.Errorf("terminal command failed")
		}
		terminal.host.recordOperation("cli", terminal.operation, terminal.started, err)
	})
}

// Close records an unfinished invocation as failed after preserving terminal cleanup.
func (terminal *timedLiveTerminal) Close() {
	terminal.liveTerminal.Close()
	terminal.finish(-1)
}
