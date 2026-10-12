package image

import (
	"io/fs"
	"os"
)

// WriteContextAndVisit writes the normalized context and visits each written relative file path.
func WriteContextAndVisit(source fs.FS, directory string, visit func(string) error) error {
	if err := WriteContext(source, directory); err != nil {
		return err
	}
	return fs.WalkDir(os.DirFS(directory), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return visit(name)
	})
}
