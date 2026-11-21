package main_test

import (
	"fmt"
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

// recordOperation appends one process result and returns its exact inclusive
// seconds for correlation, discarding all command details.
func (host liveHost) recordOperation(source, operation string, started time.Time, err error) float64 {
	if host.timings == nil {
		return time.Since(started).Seconds()
	}
	host.timings.Lock()
	defer host.timings.Unlock()
	result := "pass"
	if err != nil {
		result = "fail"
	}
	seconds := time.Since(started).Seconds()
	host.timings.evidence.Operations = append(host.timings.evidence.Operations, validationduration.Operation{Case: host.caseName, Source: source, Operation: operation, Result: result, Seconds: seconds})
	return seconds
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
		// This inclusive invocation has no trusted per-invocation build observation.
		return "image build or sandbox start"
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

// timedLiveTerminal adds a single timing to a real terminal without changing its lifetime.
type timedLiveTerminal struct {
	*liveTerminal
	host      liveHost
	started   time.Time
	operation string
	token     string
	once      sync.Once
	closeOnce sync.Once
	test      *testing.T
	seen      int
	completed bool
	result    int
	lines     liveLineStream
}

// startTerminal measures terminal CLI startup failures as well as successful invocations.
func (host liveHost) startTerminal(t *testing.T, args ...string) *timedLiveTerminal {
	t.Helper()
	started := time.Now()
	operation := liveCLIClass(args)
	environment, token := host.timingEnvironment(operation)
	ready := false
	defer func() {
		if !ready {
			seconds := host.recordOperation("cli", operation, started, fmt.Errorf("terminal startup failed"))
			host.finishTiming(token, seconds, fmt.Errorf("terminal startup failed"))
			host.observeFailure("cli", liveCLIClass(args), fmt.Errorf("terminal startup failed"))
		}
	}()
	host.reportOperation(operation, "started")
	terminal := &timedLiveTerminal{liveTerminal: startLiveTerminal(t, environment, host.binary, args...), host: host, started: started, operation: operation, token: token, test: t, lines: liveLineStream{report: host.report}}
	ready = true
	return terminal
}

// Wait records the first completed invocation after its terminal output drains
// and preserves that result across later waits, including after slow diagnostics.
// A class deadline ends an invocation that had not completed by then and reports
// finished=true with exit=-1; finished describes the host process lifetime, not a
// successful command result. An invocation that completed by the class deadline
// keeps its own result even when Wait first runs after that deadline.
func (terminal *timedLiveTerminal) Wait(timeout time.Duration) (int, bool) {
	if terminal.completed {
		return terminal.result, true
	}
	deadline := time.Now().Add(timeout)
	classDeadline := terminal.started.Add(terminal.host.operationBound(terminal.operation))
	for {
		terminal.Output()
		left := time.Until(classDeadline)
		if completed, ok := terminal.liveTerminal.completedAt(); left <= 0 && ok && !completed.After(classDeadline) {
			// Collect the on-time result; a positive wait lets a finished terminal win.
			left = 100 * time.Millisecond
		}
		if left <= 0 {
			terminal.test.Errorf("live operation %s exceeded class bound %s after %s; host invocation cancelled; engine operations may still be running: reconcile recorded objects before cleanup", terminal.operation, terminal.host.operationBound(terminal.operation), time.Since(terminal.started).Round(time.Millisecond))
			terminal.completed, terminal.result = true, -1
			terminal.Close()
			return -1, true
		}
		wait := max(time.Duration(0), min(100*time.Millisecond, time.Until(deadline), left))
		exit, finished := terminal.liveTerminal.Wait(wait)
		terminal.Output()
		if finished {
			terminal.completed, terminal.result = true, exit
			terminal.finish(exit)
			return exit, true
		}
		if !time.Now().Before(deadline) {
			return exit, false
		}
	}
}

// Output returns the captured terminal trace and streams only bytes not yet logged.
func (terminal *timedLiveTerminal) Output() string {
	output := terminal.liveTerminal.Output()
	if terminal.host.report != nil && len(output) > terminal.seen {
		terminal.lines.Write([]byte(output[terminal.seen:]))
		terminal.seen = len(output)
	}
	return output
}

// finish records one result even when the caller waits repeatedly and then closes.
func (terminal *timedLiveTerminal) finish(exit int) {
	terminal.once.Do(func() {
		terminal.lines.Flush()
		var err error
		if exit != 0 {
			err = fmt.Errorf("terminal command failed")
		}
		seconds := terminal.host.recordOperation("cli", terminal.operation, terminal.started, err)
		terminal.host.finishTiming(terminal.token, seconds, err)
		terminal.host.reportOperation(terminal.operation, fmt.Sprintf("finished after %s", time.Since(terminal.started).Round(time.Millisecond)))
		terminal.host.observeFailure("cli", terminal.operation, err)
	})
}

// Close records an unfinished invocation as failed after preserving terminal cleanup.
func (terminal *timedLiveTerminal) Close() {
	terminal.closeOnce.Do(func() { terminal.liveTerminal.Close() })
	terminal.Output()
	terminal.finish(-1)
}
