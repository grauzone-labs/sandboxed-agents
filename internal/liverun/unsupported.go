//go:build !linux && !windows

package liverun

import (
	"fmt"
	"os"
	"os/exec"
)

// freeSpace refuses unsupported host platforms.
func freeSpace(string) (uint64, error) { return 0, fmt.Errorf("live-run supports Linux and Windows") }

// lockFile refuses platforms without owned kernel locks.
func lockFile(*os.File) error { return fmt.Errorf("live-run supports Linux and Windows") }

// detachCommand is unused on unsupported host platforms.
func detachCommand(*exec.Cmd) {}

// processIdentity refuses unsupported process metadata.
func processIdentity(int) (string, error) { return "", fmt.Errorf("unsupported platform") }

// machineDisk refuses unsupported platforms.
func machineDisk(string) (string, error) { return "", fmt.Errorf("unsupported platform") }

// prepareSuite refuses unsupported process supervision.
func prepareSuite(*exec.Cmd, *os.File) (func() error, func(), error) {
	return nil, nil, fmt.Errorf("unsupported platform")
}

// suiteAlive has no supported owned process family.
func suiteAlive(ref processRef) bool { return ref.live() }

// abortSuite stops an owned process if unsupported code ever reaches startup.
func abortSuite(cmd *exec.Cmd) error { return cmd.Process.Kill() }
