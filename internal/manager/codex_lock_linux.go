package manager

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockCodexConfiguration admits concurrent managed readers or one proxy writer without waiting.
// Closing every copy of the returned descriptor releases the lock; callers must not unlink its path.
func lockCodexConfiguration(root string, exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "codex.lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open Codex configuration lock")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("Codex configuration lock must be a regular file")
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err := unix.Flock(int(file.Fd()), mode|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			if exclusive {
				return nil, errors.New("Codex is running; wait for managed runs to finish before changing the proxy")
			}
			return nil, errors.New("Codex proxy change is in progress; retry")
		}
		return nil, err
	}
	return file, nil
}
