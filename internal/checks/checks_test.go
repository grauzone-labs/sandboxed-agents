package checks_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/checks"
)

func TestChecksRunInBindingOrder(t *testing.T) {
	var visited []string
	record := func(name string) checks.Check {
		return func() error { visited = append(visited, name); return nil }
	}
	err := checks.Run(checks.Checks{
		Usage: record("usage"), Preflight: record("preflight"), LifecycleLock: record("lifecycle lock"),
		UnknownSandbox: record("unknown sandbox"), Owner: record("owner"), InterruptedUpdate: record("interrupted update"),
		StoppedSandbox: record("stopped sandbox"), Preconditions: checks.Preconditions{Manager: record("preconditions")}, Terminal: record("terminal"), SessionGuard: record("session guard"),
	})
	want := []string{"usage", "preflight", "lifecycle lock", "unknown sandbox", "owner", "interrupted update", "stopped sandbox", "preconditions", "terminal", "session guard"}
	if err != nil || !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v error=%v; want %v", visited, err, want)
	}
}

func TestChecksReportOnlyFirstFailure(t *testing.T) {
	usage := errors.New("invalid sandbox name")
	preflightCalled := false
	err := checks.Run(checks.Checks{
		Usage:     func() error { return usage },
		Preflight: func() error { preflightCalled = true; return errors.New("Podman unavailable") },
	})
	if !errors.Is(err, usage) || preflightCalled {
		t.Fatalf("error=%v preflight called=%v", err, preflightCalled)
	}
}

func TestPreconditionsRunAfterStoppedSandboxAndBeforeTerminal(t *testing.T) {
	var visited []string
	record := func(name string) checks.Check { return func() error { visited = append(visited, name); return nil } }
	err := checks.Run(checks.Checks{
		StoppedSandbox: record("stopped sandbox"),
		Preconditions:  checks.Preconditions{Manager: record("manager"), Toolchain: record("toolchain"), AgentEnabled: record("agent enabled"), UpOptions: record("up options")},
		Terminal:       record("terminal"), SessionGuard: record("session guard"),
	})
	want := []string{"stopped sandbox", "manager", "toolchain", "agent enabled", "up options", "terminal", "session guard"}
	if err != nil || !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited=%v error=%v want=%v", visited, err, want)
	}
}

func TestEveryStepStopsAtItsFirstFailure(t *testing.T) {
	for step := 1; step <= 10; step++ {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			cause := errors.New("refused")
			visited := 0
			check := func() error {
				visited++
				if visited == step {
					return cause
				}
				return nil
			}
			err := checks.Run(checks.Checks{
				Usage: check, Preflight: check, LifecycleLock: check, UnknownSandbox: check, Owner: check, InterruptedUpdate: check, StoppedSandbox: check,
				Preconditions: checks.Preconditions{Manager: check}, Terminal: check, SessionGuard: check,
			})
			var failure *checks.Failure
			if !errors.As(err, &failure) || failure.Step != step || !errors.Is(err, cause) || visited != step {
				t.Fatalf("step=%d visited=%d error=%v", step, visited, err)
			}
		})
	}
}

func TestManagerFailureSuppressesLaterPreconditions(t *testing.T) {
	cause := errors.New("manager not answering")
	laterCalled := false
	later := func() error { laterCalled = true; return errors.New("later problem") }
	err := checks.Run(checks.Checks{
		Preconditions: checks.Preconditions{Manager: func() error { return cause }, Toolchain: later, AgentEnabled: later, UpOptions: later},
		Terminal:      later, SessionGuard: later,
	})
	var failure *checks.Failure
	if !errors.As(err, &failure) || failure.Step != 8 || !errors.Is(err, cause) || laterCalled {
		t.Fatalf("error=%v later called=%v", err, laterCalled)
	}
}
