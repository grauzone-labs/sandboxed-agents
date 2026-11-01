//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// azureLoginContext cancels login and its SSH tunnel on terminal interruption or host termination.
func azureLoginContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
