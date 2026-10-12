// Command overlay writes normalized overlay inputs for the release build.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: overlay OUTPUT_DIRECTORY (run from repository root)")
		os.Exit(2)
	}
	symbol, err := writeOverlay(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(symbol)
}

func writeOverlay(directory string) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	overlay := struct{ Replace map[string]string }{Replace: make(map[string]string)}
	contextDirectory := filepath.Join(directory, "context")
	if err := image.WriteContext(image.Context(), contextDirectory); err != nil {
		return "", err
	}
	err = fs.WalkDir(os.DirFS(contextDirectory), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		replacement := filepath.Join(contextDirectory, filepath.FromSlash(path))
		overlay.Replace[filepath.Join(root, "image", "context", filepath.FromSlash(path))] = replacement
		return nil
	})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(directory, "overlay.json"), data, 0600); err != nil {
		return "", err
	}
	return release.VersionSymbol(os.DirFS(root))
}
