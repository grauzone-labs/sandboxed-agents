package scripts_test

import (
	"bytes"
	"debug/pe"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestReleaseBuildsAreReproducible proves repeat builds and a CRLF asset checkout produce identical Linux and Windows executables through the release script.
func TestReleaseBuildsAreReproducible(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release script integration requires a Linux build host")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	first, second, windowsCheckout := t.TempDir(), t.TempDir(), t.TempDir()
	buildRelease(t, root, first)
	buildRelease(t, root, second)
	copyCRLFCheckout(t, root, windowsCheckout)
	third := t.TempDir()
	buildRelease(t, windowsCheckout, third)
	for _, name := range []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe"} {
		want, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{second, third} {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs between release outputs %s and %s", name, first, dir)
			}
		}
	}
	windows, err := pe.Open(filepath.Join(first, "sandboxed-agents-windows-amd64.exe"))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.Close()
	if windows.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		t.Fatalf("Windows executable machine = %x; want AMD64", windows.Machine)
	}
	command := exec.Command(filepath.Join(third, "sandboxed-agents-linux-amd64"), "version")
	output, err := command.CombinedOutput()
	hash, hashErr := image.Hash(image.Context())
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	want := testutil.ExpectedVersion("v0.0.30-test", hash)
	if err != nil || string(output) != want {
		t.Fatalf("release version: error=%v output=%q; want %q", err, output, want)
	}
}

func buildRelease(t *testing.T, root, output string) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(root, "scripts/release.sh"), "v0.0.30-test", output)
	command.Dir = root
	// Disable automatic toolchain downloads and unrelated caller build flags.
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
}

func copyCRLFCheckout(t *testing.T, root, destination string) {
	t.Helper()
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == filepath.Join("image", "context") || relative != "." && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if relative != "go.mod" && relative != ".gitattributes" && !strings.HasSuffix(relative, ".go") && relative != filepath.Join("scripts", "release.sh") && relative != filepath.Join("scripts", "version-symbol.txt") {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if relative == filepath.Join("scripts", "version-symbol.txt") {
			data = []byte(strings.TrimSpace(string(data)) + "\r\n")
		}
		file := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		return os.WriteFile(file, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	testutil.CopyCRLFContext(t, os.DirFS(filepath.Join(root, "image", "context")), filepath.Join(destination, "image", "context"))
}
