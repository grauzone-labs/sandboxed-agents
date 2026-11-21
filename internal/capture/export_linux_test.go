package capture

// SetOwnedRecordHook makes each later tracked RunOwned call run hook with the
// started process ID just before it records the process group; nil removes it.
func SetOwnedRecordHook(hook func(pid int)) { ownedRecordHook = hook }
