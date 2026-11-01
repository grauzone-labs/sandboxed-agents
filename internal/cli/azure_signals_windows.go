package cli

import (
	"context"
	"os"
	"os/signal"
)

// azureLoginContext cancels login and its SSH tunnel on a terminal interruption.
func azureLoginContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
