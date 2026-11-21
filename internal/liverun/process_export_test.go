package liverun

// OwnedProcessLive reports whether pid still runs with the recorded creation identity, so fixtures stop only their own processes.
func OwnedProcessLive(pid int, identity string) bool {
	return processRef{PID: pid, Identity: identity}.live()
}
