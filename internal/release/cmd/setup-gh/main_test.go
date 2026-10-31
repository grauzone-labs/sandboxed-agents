package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestInstallRejectsUnverifiedArchive proves downloaded bytes whose SHA-256 differs from the pin are refused
// before anything is extracted or added to the path, through the installer with a fake download.
func TestInstallRejectsUnverifiedArchive(t *testing.T) {
	runnerTemp, githubPath := t.TempDir(), filepath.Join(t.TempDir(), "github-path")
	fetch := func(url string) ([]byte, error) {
		if url != pinnedArchive.URL {
			t.Fatalf("downloaded %s; want the pinned archive URL", url)
		}
		return []byte("untrusted archive"), nil
	}
	err := install(pinnedArchive, fetch, runnerTemp, githubPath, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "differs from the pinned") {
		t.Fatalf("untrusted archive accepted: %v", err)
	}
	if entries, _ := os.ReadDir(runnerTemp); len(entries) != 0 {
		t.Fatalf("unverified archive reached extraction: %v", entries)
	}
	if _, err := os.Stat(githubPath); !os.IsNotExist(err) {
		t.Fatalf("unverified archive reached the path: %v", err)
	}
}

// TestInstallExtractsAndPublishesPinnedArchive proves an archive with the pinned SHA-256 is extracted with its
// modes, its gh runs once, and only its bin directory is appended to GITHUB_PATH, through the installer with an
// archive built in the test.
func TestInstallExtractsAndPublishesPinnedArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the gh archive holds a Unix executable")
	}
	script := "#!/bin/sh\nprintf 'gh version %s (test)\\n' \"$GH_TEST_VERSION\"\n"
	pin := archive{Version: "9.9.9"}
	data := tarball(t, map[string]tarEntry{
		pin.directoryName() + "/":            {dir: true},
		pin.directoryName() + "/LICENSE":     {data: "license text", mode: 0644},
		pin.directoryName() + "/bin/":        {dir: true},
		pin.directoryName() + "/bin/gh":      {data: script, mode: 0755},
		pin.directoryName() + "/share/notes": {data: "notes", mode: 0644},
	})
	sum := sha256.Sum256(data)
	pin.URL, pin.SHA256 = "https://example.invalid/gh.tar.gz", hex.EncodeToString(sum[:])
	t.Setenv("GH_TEST_VERSION", "9.9.9")
	runnerTemp, githubPath := t.TempDir(), filepath.Join(t.TempDir(), "github-path")
	var stdout bytes.Buffer
	if err := install(pin, func(string) ([]byte, error) { return data, nil }, runnerTemp, githubPath, &stdout); err != nil {
		t.Fatalf("install: %v", err)
	}
	bin := filepath.Join(runnerTemp, "release-tools", pin.directoryName(), "bin")
	if stdout.String() != "gh version 9.9.9 (test)\n" {
		t.Fatalf("installed gh output = %q", stdout.String())
	}
	info, err := os.Stat(filepath.Join(bin, "gh"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installed gh mode = %v, error=%v; want 0755", info, err)
	}
	path, err := os.ReadFile(githubPath)
	if err != nil || string(path) != bin+"\n" {
		t.Fatalf("GITHUB_PATH = %q, error=%v; want %q", path, err, bin+"\n")
	}
}

// TestInstallRefusesEscapingEntries proves an archive entry that leaves the extraction directory is refused
// through the extractor, even when its SHA-256 matches.
func TestInstallRefusesEscapingEntries(t *testing.T) {
	data := tarball(t, map[string]tarEntry{"../escape": {data: "outside", mode: 0644}})
	destination := t.TempDir()
	err := extract(data, filepath.Join(destination, "tools"))
	if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("escaping entry accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escaping entry was written: %v", err)
	}
}

// TestInstallRequiresRunnerEnvironment proves a missing RUNNER_TEMP or GITHUB_PATH is refused before any
// download, through the installer with a download that fails the test.
func TestInstallRequiresRunnerEnvironment(t *testing.T) {
	fetch := func(string) ([]byte, error) { t.Fatal("downloaded without the runner environment"); return nil, nil }
	for _, test := range []struct{ runnerTemp, githubPath string }{{"", "path"}, {"temp", ""}} {
		err := install(pinnedArchive, fetch, test.runnerTemp, test.githubPath, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "RUNNER_TEMP and GITHUB_PATH are required") {
			t.Fatalf("runnerTemp=%q githubPath=%q: error=%v; want the environment refusal", test.runnerTemp, test.githubPath, err)
		}
	}
}

// TestPinnedArchiveMatchesStableGateVerifier proves the pinned gh version is the one the stable gate command
// accepts, by reading its source, so that the two pins can only change together.
func TestPinnedArchiveMatchesStableGateVerifier(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "stable-gate", "github.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `previewVerifierVersion = "`+pinnedArchive.Version+`"`) {
		t.Fatalf("stable-gate accepts another gh version than the pinned %s", pinnedArchive.Version)
	}
	if !strings.Contains(pinnedArchive.URL, "/v"+pinnedArchive.Version+"/gh_"+pinnedArchive.Version+"_linux_amd64.tar.gz") || len(pinnedArchive.SHA256) != 64 {
		t.Fatalf("pinned archive %+v does not name its version consistently", pinnedArchive)
	}
}

// tarEntry describes one entry of a test archive.
type tarEntry struct {
	dir  bool
	data string
	mode int64
}

// tarball builds a gzip-compressed tar archive with the given entries in name order.
func tarball(t *testing.T, entries map[string]tarEntry) []byte {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, name := range names {
		entry := entries[name]
		header := &tar.Header{Name: name, Mode: entry.mode, Typeflag: tar.TypeReg, Size: int64(len(entry.data))}
		if entry.dir {
			header.Typeflag, header.Mode, header.Size = tar.TypeDir, 0755, 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(writer.Close(), compressed.Close()); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
