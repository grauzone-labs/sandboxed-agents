package manager

import (
	"os"
	"syscall"
)

// sshFileOwned requires the verified worker's ownership and refuses multiply linked key files.
func sshFileOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && stat.Gid == uint32(os.Getegid()) && (info.IsDir() || stat.Nlink == 1)
}
