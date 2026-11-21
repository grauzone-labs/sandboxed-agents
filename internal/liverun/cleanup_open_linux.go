package liverun

import "syscall"

// cleanupOpenFlags opens private artifacts without waiting for a FIFO writer, so the opened descriptor can be
// rejected as nonregular instead of blocking; O_NONBLOCK does not change reads of a regular file.
const cleanupOpenFlags = syscall.O_NONBLOCK
