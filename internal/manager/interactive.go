package manager

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// watchInterrupts registers for Ctrl+C while an interactive child runs and returns the channel and the function that restores the default; both are nil when the manager has no interrupt seam.
func watchInterrupts(effects Effects) (<-chan os.Signal, func()) {
	if effects.NotifyInterrupt == nil {
		return nil, func() {}
	}
	return effects.NotifyInterrupt()
}

// interactiveFailure names the failure of an interactive child: interrupted when Ctrl+C arrived or the
// child died of a signal, otherwise failed with its exit status, otherwise unable to run; upstream's own message precedes it.
func interactiveFailure(label string, err error, interrupts <-chan os.Signal) error {
	interrupted := interruptArrived(interrupts)
	var exit interface{ ExitCode() int }
	hasExit := errors.As(err, &exit)
	switch {
	case interrupted || (hasExit && exit.ExitCode() < 0):
		return fmt.Errorf("%s was interrupted", label)
	case hasExit:
		return fmt.Errorf("%s failed with exit status %d", label, exit.ExitCode())
	}
	return fmt.Errorf("%s could not run: %w", label, err)
}

// interruptArrived reports, without waiting, whether a Ctrl+C is pending on interrupts; a nil channel never has one.
func interruptArrived(interrupts <-chan os.Signal) bool {
	if interrupts == nil {
		return false
	}
	select {
	case <-interrupts:
		return true
	default:
		return false
	}
}

// processOutput runs a process with captured streams and returns its standard output, or an error that carries the first line of its diagnostic.
func processOutput(process Process, effects Effects) (string, error) {
	var output, diagnostic bytes.Buffer
	process.Stdout, process.Stderr = &output, &diagnostic
	if err := effects.Start(process); err != nil {
		if summary := strings.TrimSpace(diagnostic.String()); summary != "" {
			return "", errors.New(strings.SplitN(summary, "\n", 2)[0])
		}
		return "", err
	}
	return output.String(), nil
}
