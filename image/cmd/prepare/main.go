// Command prepare writes release-only LF-normalized Go embed overlay inputs.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: prepare OUTPUT_DIRECTORY (run from repository root)")
		os.Exit(2)
	}
	if err := prepare(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepare(directory string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	overlay := struct{ Replace map[string]string }{Replace: make(map[string]string)}
	context := image.Context()
	err = fs.WalkDir(context, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(context, path)
		if err != nil {
			return err
		}
		replacement := filepath.Join(directory, "context", filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(replacement), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(replacement, data, 0600); err != nil {
			return err
		}
		overlay.Replace[filepath.Join(root, "image", "context", filepath.FromSlash(path))] = replacement
		return nil
	})
	if err != nil {
		return err
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "overlay.json"), data, 0600)
}
