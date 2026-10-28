package manager

import (
	"os"
	"syscall"
)

// sshFileOwned compares actual file ownership with the process's effective OS UID/GID
// and refuses multiply linked key files. This real-filesystem check is independent
// of Effects' injected identity; production verifies the agent identity before home access.
func sshFileOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Gid == uint32(os.Getegid()) && (info.IsDir() || stat.Nlink == 1)
}
