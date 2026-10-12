package release

import (
	"io/fs"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

// WriteContextAndVisit writes the normalized context and visits each written relative file path.
func WriteContextAndVisit(source fs.FS, directory string, visit func(string) error) error {
	if err := image.WriteContext(source, directory); err != nil {
		return err
	}
	return fs.WalkDir(os.DirFS(directory), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return visit(name)
	})
}
