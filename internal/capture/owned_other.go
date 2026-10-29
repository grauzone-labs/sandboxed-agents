//go:build !linux && !windows

package capture

import (
	"fmt"
	"os/exec"
)

// prepareOwned refuses supervision on unsupported hosts instead of promising tree cancellation.
func prepareOwned(command *exec.Cmd) (func() error, func() error, func(), error) {
	return nil, nil, nil, fmt.Errorf("owned process supervision requires Linux or Windows")
}
