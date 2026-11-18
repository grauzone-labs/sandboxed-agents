package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// acquireImageLock waits for the image tag's exclusive lock across all controller groups
// sharing this host-state root. Callers inspect the tag only after acquiring it.
func acquireImageLock(p podmanTarget, tag string) (*os.File, error) {
	root, err := hostStateRoot(p.host.Platform)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(tag))
	return acquireHostLockWaiting(filepath.Join(root, "locks", "images"), fmt.Sprintf("%x", hash), "image")
}

// acquirePortAllocationLock waits for the common host port-allocation lock. A new sandbox
// holds it from its recorded-port inventory through creation of its port-labelled container.
func acquirePortAllocationLock(p podmanTarget) (*os.File, error) {
	root, err := hostStateRoot(p.host.Platform)
	if err != nil {
		return nil, err
	}
	return acquireHostLockWaiting(filepath.Join(root, "locks"), "ports", "port allocation")
}
