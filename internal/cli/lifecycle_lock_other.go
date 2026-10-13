//go:build !linux && !windows

package cli

import (
	"fmt"
	"os"
)

func lockLifecycleFile(file *os.File) error {
	return fmt.Errorf("lifecycle locking requires Linux or Windows")
}

func lifecycleLockBusy(err error) bool { return false }
