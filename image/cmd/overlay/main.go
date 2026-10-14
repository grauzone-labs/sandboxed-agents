// Command overlay writes normalized overlay inputs for the release build.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/grauzone-labs/sandboxed-agents/image"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: overlay OUTPUT_DIRECTORY (run from repository root)")
		os.Exit(2)
	}
	err := writeOverlay(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeOverlay(directory string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	overlay := struct{ Replace map[string]string }{Replace: make(map[string]string)}
	contextDirectory := filepath.Join(directory, "context")
	err = image.WriteContextAndVisit(image.Context(), contextDirectory, func(path string) error {
		replacement := filepath.Join(contextDirectory, filepath.FromSlash(path))
		overlay.Replace[filepath.Join(root, "image", "context", filepath.FromSlash(path))] = replacement
		return nil
	})
	if err != nil {
		return err
	}
	// Keep the generated manager out of the checkout. Both host platforms embed these exact Linux bytes.
	manager := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", filepath.Join(contextDirectory, "base", "manager.bin"), "./cmd/sandboxed-agents-manager")
	manager.Dir = root
	manager.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOAMD64=v1", "GOTOOLCHAIN=local", "GOFLAGS=", "GOEXPERIMENT=", "GOWORK=off", "GOENV=off", "GOFIPS140=off")
	if output, err := manager.CombinedOutput(); err != nil {
		return fmt.Errorf("build embedded Linux manager: %w\n%s", err, output)
	}
	data, err := json.Marshal(overlay)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "overlay.json"), data, 0600)
}
