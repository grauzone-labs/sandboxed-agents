// Command package builds the reproducible npm package from release executables.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: package VERSION REPOSITORY OUTPUT_DIRECTORY")
		os.Exit(2)
	}
	if err := build(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "npm package:", err)
		os.Exit(1)
	}
}

func build(version, root, output string) error {
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "launcher.cjs", "verify.cjs"} {
		data, err := os.ReadFile(filepath.Join(root, "packages", "npm", name))
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		if name == "package.json" {
			data = bytes.ReplaceAll(data, []byte("$version$"), []byte(version))
			if !json.Valid(data) {
				return fmt.Errorf("invalid package metadata")
			}
		}
		files[name] = data
	}
	for _, name := range []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe", "SHA256SUMS"} {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			return err
		}
		files[name] = data
	}
	return writeArchive(filepath.Join(output, "sandboxed-agents-"+version+".tgz"), files)
}

func writeArchive(destination string, files map[string][]byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".npm-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := int64(0644)
		if name == "launcher.cjs" || name == "sandboxed-agents-linux-amd64" {
			mode = 0755
		}
		header := &tar.Header{Name: "package/" + name, Mode: mode, Size: int64(len(files[name])), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(files[name]); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}
