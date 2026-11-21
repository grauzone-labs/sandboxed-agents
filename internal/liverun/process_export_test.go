package liverun

import "os"

// LockRunFile takes the same exclusive nonblocking kernel lock that run phases use, so tests can hold a phase lock.
func LockRunFile(file *os.File) error { return lockFile(file) }

// OwnedProcessLive reports whether pid still runs with the recorded creation identity, so fixtures stop only their own processes.
func OwnedProcessLive(pid int, identity string) bool {
	return processRef{PID: pid, Identity: identity}.live()
}
