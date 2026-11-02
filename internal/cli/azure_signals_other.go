//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// azureLoginContext cancels login and its SSH tunnel on interruption, terminal hangup or host termination.
func azureLoginContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
}
