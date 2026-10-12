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
	"testing/fstest"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

const releaseVersion = "v0.0.30-test"

// These artifact names mirror scripts/release.sh's sandboxed-agents-$target-amd64$suffix pattern.
const (
	linuxArtifact   = "sandboxed-agents-linux-amd64"
	windowsArtifact = "sandboxed-agents-windows-amd64.exe"
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
	for _, name := range []string{linuxArtifact, windowsArtifact} {
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
	windows, err := pe.Open(filepath.Join(first, windowsArtifact))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.Close()
	if windows.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		t.Fatalf("Windows executable machine = %x; want AMD64", windows.Machine)
	}
	command := exec.Command(filepath.Join(third, linuxArtifact), "version")
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("release version: error=%v output=%q; want %q", err, output, want)
	}
}

// TestReleaseVersionSymbolAcceptsCRLFMetadata proves the Go metadata reader and actual release script use the same canonical linker target from CRLF metadata through their build interfaces.
func TestReleaseVersionSymbolAcceptsCRLFMetadata(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release script integration requires a Linux build host")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(root, "scripts", "version-symbol.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantSymbol := strings.TrimSuffix(string(metadata), "\n")
	checkout := t.TempDir()
	copyCRLFCheckout(t, root, checkout)
	if err := os.WriteFile(filepath.Join(checkout, "scripts", "version-symbol.txt"), []byte(wantSymbol+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gotSymbol, err := release.VersionSymbol(os.DirFS(checkout))
	if err != nil || gotSymbol != wantSymbol {
		t.Fatalf("metadata symbol = %q, error=%v; want %q", gotSymbol, err, wantSymbol)
	}
	reader := exec.Command("go", "run", "./internal/release/cmd/version-symbol")
	reader.Dir = checkout
	reader.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off")
	symbolOutput, err := reader.CombinedOutput()
	if err != nil || string(symbolOutput) != gotSymbol+"\n" {
		t.Fatalf("build command symbol = %q, error=%v; want Go reader symbol %q", symbolOutput, err, gotSymbol+"\n")
	}
	outputDirectory := t.TempDir()
	buildRelease(t, checkout, outputDirectory)
	command := exec.Command(filepath.Join(outputDirectory, linuxArtifact), "version")
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("CRLF metadata release version: error=%v output=%q; want %q", err, output, want)
	}
}

func buildRelease(t *testing.T, root, output string) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(root, "scripts/release.sh"), releaseVersion, output)
	command.Dir = root
	// Disable automatic toolchain downloads and unrelated caller build flags.
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
}

func copyCRLFCheckout(t *testing.T, root, destination string) {
	t.Helper()
	source := fstest.MapFS{}
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
		source[filepath.ToSlash(relative)] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := image.WriteContext(source, destination); err != nil {
		t.Fatal(err)
	}
	releasetest.CopyCRLFContext(t, os.DirFS(filepath.Join(root, "image", "context")), filepath.Join(destination, "image", "context"))
}
