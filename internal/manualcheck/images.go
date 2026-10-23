package manualcheck

import (
	"fmt"
	"path/filepath"
	"regexp"
)

var inventoryName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ImagesInventory takes a named image inventory for item `no-image-removal`.
func (r *Run) ImagesInventory(name string) (string, error) {
	if !inventoryName.MatchString(name) {
		return "", fmt.Errorf("invalid inventory name %q", name)
	}
	dir, err := r.ItemDir("no-image-removal")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".txt")
	if err := r.ImageInventory(path); err != nil {
		return "", err
	}
	ids, err := ImageIDs(path)
	if err != nil {
		return "", err
	}
	sha, err := FileSHA256(path)
	if err != nil {
		return "", err
	}
	r.Printf("inventory %s: %d images, sha256 %s\n", path, len(ids), sha)
	return path, nil
}

// ImagesCompare compares two named inventories for item `no-image-removal`: every image ID of the
// first must still be present in the second.
func (r *Run) ImagesCompare(before, after string) error {
	dir, err := r.ItemDir("no-image-removal")
	if err != nil {
		return err
	}
	missing, err := CompareImages(filepath.Join(dir, before+".txt"), filepath.Join(dir, after+".txt"))
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := r.Verdict(dir, "no-image-removal", true, fmt.Sprintf("%d image(s) of %s are missing from %s: %v; the item fails", len(missing), before, after, missing)); err != nil {
			return err
		}
		return fmt.Errorf("no-image-removal: %d image(s) removed", len(missing))
	}
	ids, _ := ImageIDs(filepath.Join(dir, before+".txt"))
	return r.Verdict(dir, "no-image-removal", false, fmt.Sprintf("every image ID of %s (%d) is present in %s", before, len(ids), after))
}
