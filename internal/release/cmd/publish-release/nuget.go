package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// compareNuGet permits nuget.org's added repository signature only after signature verification and an exact comparison of every original entry's bytes.
func (p publication) compareNuGet(name, path string, data []byte) error {
	want, err := os.ReadFile(filepath.Join(p.directory, name))
	if err != nil {
		return err
	}
	if bytes.Equal(data, want) {
		return nil
	}
	original, err := zip.NewReader(bytes.NewReader(want), int64(len(want)))
	if err != nil {
		return err
	}
	signed, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("published %s is not a NuGet package", name)
	}
	expected := map[string]*zip.File{}
	for _, file := range original.File {
		if expected[file.Name] != nil || file.Name == ".signature.p7s" {
			return fmt.Errorf("rebuilt NuGet package contains ambiguous entries or a signature")
		}
		expected[file.Name] = file
	}
	seen := map[string]bool{}
	for _, file := range signed.File {
		if seen[file.Name] {
			return fmt.Errorf("published NuGet package contains duplicate entries")
		}
		seen[file.Name] = true
		if file.Name == ".signature.p7s" {
			if file.UncompressedSize64 == 0 {
				return fmt.Errorf("NuGet signature is empty")
			}
			continue
		}
		before := expected[file.Name]
		if before == nil || before.UncompressedSize64 != file.UncompressedSize64 {
			return fmt.Errorf("published %s content differs from rebuilt package", name)
		}
		left, err := zipEntry(before)
		if err != nil {
			return err
		}
		right, err := zipEntry(file)
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return fmt.Errorf("published %s content differs from rebuilt package", name)
		}
	}
	if !seen[".signature.p7s"] || len(seen) != len(expected)+1 {
		return fmt.Errorf("published %s content or repository signature is missing", name)
	}
	if _, err := command("dotnet", "nuget", "verify", path, "--all"); err != nil {
		return fmt.Errorf("NuGet repository signature verification: %w", err)
	}
	return nil
}

// zipEntry reads one verified-size archive entry and checks its CRC through the ZIP reader.
func zipEntry(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
