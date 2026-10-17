// Command package builds the reproducible NuGet command package from release executables.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	windowsBinary     = "sandboxed-agents-windows-amd64.exe"
	versionNumber     = `(?:0|[1-9][0-9]*)`
	versionIdentifier = `(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)`
	contentTypes      = `<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml" />
  <Default Extension="nuspec" ContentType="application/octet-stream" />
  <Default Extension="ps1" ContentType="text/plain" />
  <Default Extension="exe" ContentType="application/octet-stream" />
  <Override PartName="/tools/SHA256SUMS" ContentType="text/plain" />
</Types>
`
	relationships = `<?xml version="1.0" encoding="utf-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Type="http://schemas.microsoft.com/packaging/2010/07/manifest" Target="/SandboxedAgents.nuspec" Id="manifest" />
</Relationships>
`
)

// main builds the requested NuGet command package or exits with a usage or packaging error.
func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: package VERSION REPOSITORY OUTPUT_DIRECTORY")
		os.Exit(2)
	}
	if err := build(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "NuGet package:", err)
		os.Exit(1)
	}
}

// build validates the version, assembles the Windows command package, and writes release checksums.
func build(version, root, output string) error {
	pattern := `^` + versionNumber + `\.` + versionNumber + `\.` + versionNumber + `(?:-` + versionIdentifier + `(?:\.` + versionIdentifier + `)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`
	if !regexp.MustCompile(pattern).MatchString(version) {
		return fmt.Errorf("version %q must be X.Y.Z with optional prerelease and build metadata", version)
	}
	files := map[string][]byte{
		"[Content_Types].xml": []byte(contentTypes),
		"_rels/.rels":         []byte(relationships),
	}
	var checksums strings.Builder
	for _, name := range []string{"sandboxed-agents-linux-amd64", windowsBinary} {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(data), name)
		if name == windowsBinary {
			files["tools/"+name] = data
		}
	}
	files["tools/SHA256SUMS"] = []byte(checksums.String())
	for _, name := range []string{"SandboxedAgents.nuspec", "tools/install-command.ps1", "tools/remove-command.ps1", "tools/command-environment.ps1"} {
		data, err := os.ReadFile(filepath.Join(root, "packages", "nuget", filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		if name == "SandboxedAgents.nuspec" {
			data = bytes.ReplaceAll(data, []byte("$version$"), []byte(version))
		}
		files[name] = data
	}
	if err := os.WriteFile(filepath.Join(output, "SHA256SUMS"), []byte(checksums.String()), 0644); err != nil {
		return err
	}
	filenameVersion, _, _ := strings.Cut(version, "+")
	return writeArchive(filepath.Join(output, "SandboxedAgents."+filenameVersion+".nupkg"), files)
}

// writeArchive writes sorted ZIP entries with fixed metadata and atomically replaces destination.
func writeArchive(destination string, files map[string][]byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".nuget-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	archive := zip.NewWriter(file)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0644)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(files[name]); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
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
