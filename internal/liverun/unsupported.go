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
func detachCommand(*exec.Cmd, SessionPolicy) {}

// processIdentity refuses unsupported process metadata.
func processIdentity(int) (string, error) { return "", fmt.Errorf("unsupported platform") }

// machineDisk refuses unsupported platforms.
func machineDisk(string) (string, error) { return "", fmt.Errorf("unsupported platform") }

// prepareSuite refuses unsupported process supervision.
func prepareSuite(*exec.Cmd, *os.File) (func() error, func(), error) {
	return nil, nil, fmt.Errorf("unsupported platform")
}

// suiteEndsWithSupervisor is false where no owned process family is supported.
const suiteEndsWithSupervisor = false

// suiteAlive has no supported owned process family.
func suiteAlive(ref processRef) bool { return ref.live() }

// abortSuite stops an owned process if unsupported code ever reaches startup.
func abortSuite(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// sessionJobPolicy refuses unimplemented host session policy inspection.
func sessionJobPolicy() (SessionPolicy, error) {
	return SessionPolicy{}, fmt.Errorf("unsupported host session inspection")
}

// replaceBusy reports no transient replacement failure on unsupported host platforms.
func replaceBusy(error) bool { return false }

// prepareOwnedGroups records no invocation groups where no owned process family is supported.
func prepareOwnedGroups(string) (string, error) { return "", nil }

// abortOwnedGroups has no recorded invocation groups to end on an unsupported host.
func abortOwnedGroups(string) {}
