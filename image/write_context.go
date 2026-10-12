package image

import (
	"io/fs"
	"os"
	"path/filepath"
)

// WriteContext writes an LF-normalized image build context into directory, preserving binary asset bytes.
func WriteContext(source fs.FS, directory string) error {
	_, err := writeContext(source, directory)
	return err
}

func writeContext(source fs.FS, directory string) ([]string, error) {
	return walkAssets(source, func(name string, data []byte) error {
		file := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	})
}

// WriteContextAndVisit writes the complete normalized context, then visits each written relative file path.
func WriteContextAndVisit(source fs.FS, directory string, visit func(string) error) error {
	paths, err := writeContext(source, directory)
	if err != nil {
		return err
	}
	for _, name := range paths {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}
