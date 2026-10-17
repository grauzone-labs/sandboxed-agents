// Package checks defines the binding check order for every host command.
package checks

import "fmt"

// Check is a validation or guard; nil is an unimplemented passing check.
type Check func() error

// Checks supplies command guards in the binding ten-step order.
type Checks struct {
	Usage, Preflight, LifecycleLock, UnknownSandbox, Owner, InterruptedUpdate, StoppedSandbox Check
	Preconditions                                                                             Preconditions
	Terminal, SessionGuard                                                                    Check
}

// Run executes non-nil guards in order and returns the first failure with its step.
func Run(checks Checks) error {
	for index, check := range []Check{
		checks.Usage, checks.Preflight, checks.LifecycleLock, checks.UnknownSandbox, checks.Owner,
		checks.InterruptedUpdate, checks.StoppedSandbox, checks.Preconditions.run, checks.Terminal, checks.SessionGuard,
	} {
		if check != nil {
			if err := check(); err != nil {
				return &Failure{Step: index + 1, Cause: err}
			}
		}
	}
	return nil
}

// Failure retains the first failing step and its cause.
type Failure struct {
	Step  int
	Cause error
}

// Error reports the failing step and its cause.
func (f *Failure) Error() string { return fmt.Sprintf("step %d: %v", f.Step, f.Cause) }

// Unwrap exposes the underlying guard failure for error inspection.
func (f *Failure) Unwrap() error { return f.Cause }

// Preconditions is step 8's fixed order, with the manager checked first.
type Preconditions struct {
	Manager, Toolchain, AgentEnabled, UpOptions Check
}

// run evaluates non-nil preconditions in order, stopping at the first error.
func (p Preconditions) run() error {
	for _, check := range []Check{p.Manager, p.Toolchain, p.AgentEnabled, p.UpOptions} {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
	}
	return nil
}
