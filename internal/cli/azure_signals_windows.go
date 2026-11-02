package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// azureLoginContext cancels login on terminal interruption or Windows console-close notification.
func azureLoginContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
