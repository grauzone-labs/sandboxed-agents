package cli

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// acquireImageTagLock waits for the image tag's exclusive lock across all controller groups
// sharing this host-state root, reporting contention on progress. Callers inspect the tag only
// after acquiring it.
func acquireImageTagLock(p podmanTarget, tag string, progress io.Writer) (*os.File, error) {
	root, err := hostStateRoot(p.host.Platform)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(tag))
	return acquireHostLockWaiting(filepath.Join(root, "locks", "images"), fmt.Sprintf("%x", hash), "image-tag", progress)
}

// acquirePortAllocationLock waits for the common host port-allocation lock. A new sandbox
// holds it from its recorded-port inventory through creation of its port-labelled container.
// Contention is reported once on progress.
func acquirePortAllocationLock(p podmanTarget, progress io.Writer) (*os.File, error) {
	root, err := hostStateRoot(p.host.Platform)
	if err != nil {
		return nil, err
	}
	return acquireHostLockWaiting(filepath.Join(root, "locks"), "ports", "port-allocation", progress)
}
