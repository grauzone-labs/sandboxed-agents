//go:build !linux && !windows

package cli

import (
	"fmt"
	"os"
)

func hideTerminalInput(*os.File) (func() error, error) {
	return nil, fmt.Errorf("secure terminal input is unsupported on this platform")
}
