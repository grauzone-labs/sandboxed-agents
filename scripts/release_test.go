package scripts_test

import (
	"bytes"
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// These artifact names mirror scripts/release.sh's sandboxed-agents-$target-amd64$suffix pattern.
const (
	linuxArtifact   = "sandboxed-agents-linux-amd64"
	windowsArtifact = "sandboxed-agents-windows-amd64.exe"
)

// TestReleaseBuildsAreReproducible proves repeat builds and CRLF assets, module metadata, and version-symbol metadata produce identical Linux and Windows executables through the release script.
func TestReleaseBuildsAreReproducible(t *testing.T) {
	root := releaseRepository(t)
	first, second, crlfCheckout := t.TempDir(), t.TempDir(), t.TempDir()
	buildRelease(t, root, first)
	buildRelease(t, root, second)
	copyRepositoryWithCRLFAssetsAndMetadata(t, root, crlfCheckout)
	module, err := os.ReadFile(filepath.Join(crlfCheckout, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(module, []byte("\r\n")) {
		t.Fatal("CRLF checkout go.mod contains no CRLF line endings")
	}
	crlfArtifacts := t.TempDir()
	buildRelease(t, crlfCheckout, crlfArtifacts)
	for _, name := range []string{linuxArtifact, windowsArtifact} {
		want, err := os.ReadFile(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{second, crlfArtifacts} {
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
	assertReleaseVersion(t, crlfArtifacts)
}

// TestReleaseVersionSymbolAcceptsCRLFMetadata proves the Go metadata reader and actual release script use the same canonical linker target from CRLF metadata through their build interfaces.
func TestReleaseVersionSymbolAcceptsCRLFMetadata(t *testing.T) {
	root := releaseRepository(t)
	metadata, err := os.ReadFile(filepath.Join(root, "scripts", "version-symbol.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantSymbol := strings.TrimSuffix(string(metadata), "\n")
	crlfCheckout := t.TempDir()
	copyRepositoryWithCRLFAssetsAndMetadata(t, root, crlfCheckout)
	gotSymbol, err := release.VersionSymbol(os.DirFS(crlfCheckout))
	if err != nil || gotSymbol != wantSymbol {
		t.Fatalf("metadata symbol = %q, error=%v; want %q", gotSymbol, err, wantSymbol)
	}
	reader := exec.Command("go", "run", "./internal/release/cmd/version-symbol")
	reader.Dir = crlfCheckout
	reader.Env = releaseTestEnvironment()
	symbolOutput, err := reader.CombinedOutput()
	if err != nil || string(symbolOutput) != gotSymbol+"\n" {
		t.Fatalf("build command symbol = %q, error=%v; want Go reader symbol %q", symbolOutput, err, gotSymbol+"\n")
	}
	outputDirectory := t.TempDir()
	buildRelease(t, crlfCheckout, outputDirectory)
	assertReleaseVersion(t, outputDirectory)
}

func releaseRepository(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("release script integration requires a Linux build host")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// releaseTestEnvironment prevents toolchain downloads (GOTOOLCHAIN=local), clears caller build flags (GOFLAGS), and disables the module proxy (GOPROXY=off) and checksum database (GOSUMDB=off).
func releaseTestEnvironment() []string {
	return append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off")
}

func assertReleaseVersion(t *testing.T, directory string) {
	t.Helper()
	command := exec.Command(filepath.Join(directory, linuxArtifact), "version")
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releasetest.ReleaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("release version: error=%v output=%q; want %q", err, output, want)
	}
}

func buildRelease(t *testing.T, root, output string) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(root, "scripts/release.sh"), releasetest.ReleaseVersion, output)
	command.Dir = root
	command.Env = releaseTestEnvironment()
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
}

// copyRepositoryWithCRLFAssetsAndMetadata keeps source files at normalized LF, then sets image context text, go.mod, and version-symbol.txt to CRLF while preserving binary assets.
func copyRepositoryWithCRLFAssetsAndMetadata(t *testing.T, root, destination string) {
	t.Helper()
	releasetest.CopyRepositorySources(t, root, destination)
	releasetest.CopyCRLFContext(t, os.DirFS(filepath.Join(root, "image", "context")), filepath.Join(destination, "image", "context"))
	for _, relative := range []string{"go.mod", filepath.Join("scripts", "version-symbol.txt")} {
		name := filepath.Join(destination, relative)
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, releasetest.ToCRLF(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
