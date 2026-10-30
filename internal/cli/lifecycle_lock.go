package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// hostStateError marks a failure to resolve the host state root: a problem of the host
// environment that no command usage fixes. Its message is its cause's.
type hostStateError struct{ cause error }

// Error returns the message of the underlying cause unchanged.
func (e hostStateError) Error() string { return e.cause.Error() }

// Unwrap exposes the underlying cause for error inspection.
func (e hostStateError) Unwrap() error { return e.cause }

// hostStateRoot resolves host state, ignoring relative XDG_STATE_HOME values
// on Linux and requiring an absolute LOCALAPPDATA path on Windows. Each failure is a
// hostStateError.
func hostStateRoot(platform string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if platform == "windows" {
		root = os.Getenv("LOCALAPPDATA")
		if root == "" {
			return "", hostStateError{fmt.Errorf("LOCALAPPDATA is required for host state")}
		}
	} else if root == "" || !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", hostStateError{err}
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", hostStateError{fmt.Errorf("host state path must be absolute: %s", root)}
	}
	return filepath.Join(root, "sandboxed-agents"), nil
}

// errHostLockBusy reports that another process holds a host lock that was requested without
// waiting.
var errHostLockBusy = errors.New("host lock is held by another process")

// hostLockFailure marks an error of the lock call itself, as opposed to preparing the lock file,
// so callers can name the locked object in its message. Its message is its cause's.
type hostLockFailure struct{ cause error }

// Error returns the message of the underlying cause unchanged.
func (e hostLockFailure) Error() string { return e.cause.Error() }

// Unwrap exposes the underlying cause for error inspection.
func (e hostLockFailure) Unwrap() error { return e.cause }

// acquireLifecycleLock opens a regular lock file for the sandbox and acquires its lifecycle lock
// without waiting.
func acquireLifecycleLock(root string, request Request) (*os.File, error) {
	hash := sha256.Sum256([]byte(DerivedNames(request.Group, request.Name).Container))
	file, err := acquireHostLock(filepath.Join(root, "group-"+request.Group, "locks"), fmt.Sprintf("%x", hash), "lifecycle")
	var failure hostLockFailure
	if errors.Is(err, errHostLockBusy) {
		return nil, fmt.Errorf("sandbox %q: another lifecycle or SSH setup command is in progress; retry", request.Name)
	} else if errors.As(err, &failure) {
		return nil, fmt.Errorf("lock sandbox %q: %w", request.Name, failure.cause)
	}
	return file, err
}

// acquireSSHConfigLock opens the controller group's regular ssh-config lock file in dir, the
// group's locks directory, and acquires it without waiting. It serialises every rebuild of the
// group's managed SSH configuration and the user's Include. Callers take it only while already
// holding the sandbox's lifecycle lock, never the reverse, and close the file to release it.
func acquireSSHConfigLock(dir, group string) (*os.File, error) {
	file, err := acquireHostLock(dir, "ssh-config", "SSH configuration")
	var failure hostLockFailure
	if errors.Is(err, errHostLockBusy) {
		return nil, fmt.Errorf("controller group %q: another SSH setup command is in progress; retry", group)
	} else if errors.As(err, &failure) {
		return nil, fmt.Errorf("lock SSH configuration of controller group %q: %w", group, failure.cause)
	}
	return file, err
}

// acquireHostLock creates dir when missing, opens the regular lock file name inside it, and takes
// its exclusive lock without waiting. It returns errHostLockBusy when another process holds the
// lock, a hostLockFailure when the lock call fails otherwise, and an error naming the lock kind
// when the lock file cannot be prepared.
func acquireHostLock(dir, name, kind string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create %s lock directory: %w", kind, err)
	}
	path := filepath.Join(dir, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s lock path is not a regular file: %s", kind, path)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open %s lock: %w", kind, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s lock path is not a regular file: %s", kind, path)
	}
	if err := lockLifecycleFile(file); err != nil {
		file.Close()
		if lifecycleLockBusy(err) {
			return nil, errHostLockBusy
		}
		return nil, hostLockFailure{err}
	}
	return file, nil
}
