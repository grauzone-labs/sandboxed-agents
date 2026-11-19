package liverun

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// engineKey gives the same per-user engine one lock across checkouts and controller groups.
func engineKey(engine string) string {
	sum := sha256.Sum256([]byte(engine))
	return hex.EncodeToString(sum[:])
}

// acquireEngine takes an operating-system lock whose lifetime ends even after a killed owner.
func acquireEngine(root, engine string) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, engineKey(engine)+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("another live suite holds the engine or lock cannot be verified: %w", err)
	}
	return file, nil
}

// processRef identifies one owned process without trusting a reused PID.
type processRef struct {
	PID      int
	Identity string
}

// live reports whether the recorded process still exists with the same creation identity.
func (p processRef) live() bool {
	if p.PID <= 0 || p.Identity == "" {
		return false
	}
	identity, err := processIdentity(p.PID)
	return err == nil && identity == p.Identity
}

// ownedProcess obtains the platform's creation identity for a process started by this invocation.
func ownedProcess(pid int) (processRef, error) {
	identity, err := processIdentity(pid)
	return processRef{PID: pid, Identity: identity}, err
}

// activeRun describes the processes owned by one current suite and its retained run directory.
type activeRun struct {
	Supervisor processRef
	Child      processRef
	RunDir     string
}

// activePath returns the stable engine-wide private ownership artifact.
func activePath(o Options, engine string) string {
	return filepath.Join(o.LockRoot, engineKey(engine)+"-active.json")
}

// refuseActive prevents a new run while a previously owned supervisor or suite still lives.
func refuseActive(o Options, engine string) error {
	var active activeRun
	if err := readJSON(activePath(o, engine), &active); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot verify previous owned suite")
	}
	if active.Supervisor.live() || suiteAlive(active.Child) {
		return fmt.Errorf("another owned live suite is still running")
	}
	return nil
}

// coordinationRoot binds production phases to one per-user cache namespace.
// Different OS users or different cache environments do not share this contract;
// filesystem ownership does not protect coordination from other writers of that user.
func (s *Service) coordinationRoot() (string, error) {
	if s.CoordinationRoot != "" {
		return filepath.Abs(s.CoordinationRoot)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(cache, "sandboxed-agents", "live-run"))
}

// checkCoordination refuses old or edited artifacts that could split run and cleanup locking.
func (s *Service) checkCoordination(o Options) error {
	root, err := s.coordinationRoot()
	if err != nil || root != o.LockRoot {
		return fmt.Errorf("coordination root changed; prepare with the current host user and cache environment")
	}
	return nil
}
