package scripts_test

import (
	"bytes"
	"debug/pe"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// These artifact names mirror the build-release command's sandboxed-agents-TARGET-amd64 pattern.
const (
	linuxArtifact   = "sandboxed-agents-linux-amd64"
	windowsArtifact = "sandboxed-agents-windows-amd64.exe"
)

var releaseArtifacts = []string{linuxArtifact, windowsArtifact, "SHA256SUMS", "SandboxedAgents." + releasetest.ReleaseVersion + ".nupkg", "sandboxed-agents-" + releasetest.ReleaseVersion + ".tgz"}

// TestReleaseBuildsAreReproducible proves the same tagged commit yields identical executables, checksums, NuGet packages, and npm tarballs from LF and CRLF inputs through the release build command on Linux.
func TestReleaseBuildsAreReproducible(t *testing.T) {
	assertReproducibleRelease(t, releaseRepositoryOrSkip(t))
}

// TestWindowsReleaseBuildsAreReproducible proves the same tagged commit yields identical executable, NuGet package, and npm tarball bytes and a working Windows version command through the build-release command on a Windows host.
func TestWindowsReleaseBuildsAreReproducible(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows release integration requires Windows")
	}
	assertReproducibleRelease(t, repositoryRoot(t))
}

// assertReproducibleRelease checks release artifacts match across repeated builds and a CRLF checkout.
func assertReproducibleRelease(t *testing.T, root string) {
	t.Helper()
	baseline := createReleaseRepository(t, root)
	tagReleaseRepository(t, baseline)
	first, second := t.TempDir(), t.TempDir()
	buildRelease(t, baseline, first)
	buildRelease(t, baseline, second)
	crlfCheckout := cloneReleaseRepository(t, baseline)
	rewriteReleaseInputsToCRLF(t, crlfCheckout)
	expectedCommit := gitOutput(t, baseline, "rev-parse", "HEAD")
	for _, ref := range []string{"HEAD", "refs/tags/v" + releasetest.ReleaseVersion + "^{commit}"} {
		if got := gitOutput(t, crlfCheckout, "rev-parse", ref); got != expectedCommit {
			t.Fatalf("CRLF checkout %s = %s; want baseline commit %s", ref, got, expectedCommit)
		}
	}
	crlfArtifacts := t.TempDir()
	buildRelease(t, crlfCheckout, crlfArtifacts)
	assertReleaseArtifactsEqual(t, first, second, crlfArtifacts)
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

// TestReleaseRefusesMissingVersionTag proves an untagged commit is refused through the actual release build command before outputs are built.
func TestReleaseRefusesMissingVersionTag(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	checkout := createReleaseRepository(t, root)
	assertReleaseRefused(t, checkout, "release requires tag refs/tags/v"+releasetest.ReleaseVersion)
}

// TestReleaseRefusesVersionTagOnAnotherCommit proves a tag targeting a previous commit is refused through the actual release build command before outputs are built.
func TestReleaseRefusesVersionTagOnAnotherCommit(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	checkout := createReleaseRepository(t, root)
	tagReleaseRepository(t, checkout)
	runGit(t, checkout, "commit", "--quiet", "--allow-empty", "-m", "test(release): advance fixture HEAD")
	assertReleaseRefused(t, checkout, "release tag refs/tags/v"+releasetest.ReleaseVersion+" does not point to HEAD")
}

// TestReleaseRefusesUntrackedFiles proves an untracked file in a tagged temporary worktree is refused through the actual release build command before outputs are built.
func TestReleaseRefusesUntrackedFiles(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("extra input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertReleaseRefused(t, checkout, "release requires a clean Git worktree")
}

// TestReleaseRefusesIgnoredContextFiles proves an ignored file embedded from image/context is refused through the actual release build command in a clean tagged temporary worktree.
func TestReleaseRefusesIgnoredContextFiles(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	writeIgnoredFile(t, checkout, "image/context/ignored-input.txt", []byte("ignored asset\n"))
	assertReleaseRefused(t, checkout, "release refuses ignored release inputs")
}

// TestReleaseAllowsIgnoredFilesOutsideInputs proves the same ignored filename outside release inputs remains buildable through the actual release build command in a tagged temporary worktree.
func TestReleaseAllowsIgnoredFilesOutsideInputs(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	writeIgnoredFile(t, checkout, "cache/ignored-input.txt", []byte("irrelevant cache\n"))
	output := t.TempDir()
	buildRelease(t, checkout, output)
	assertReleaseVersion(t, output)
}

// TestReleaseRefusesIgnoredGoSources proves root and nested ignored Go inputs are refused through the actual release build command in otherwise clean tagged temporary worktrees.
func TestReleaseRefusesIgnoredGoSources(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	for _, name := range []string{"ignored.go", "image/ignored.go"} {
		t.Run(name, func(t *testing.T) {
			checkout := createReleaseRepository(t, root)
			tagReleaseRepository(t, checkout)
			writeIgnoredFile(t, checkout, name, []byte("package image\n"))
			assertReleaseRefused(t, checkout, "release refuses ignored release inputs")
		})
	}
}

// TestReleaseIsolatesWorkspaceConfiguration proves ignored root workspaces are refused and a caller workspace and Go environment file, which the go command that launches the build honours, cannot alter tagged release bytes through the actual release build command.
func TestReleaseIsolatesWorkspaceConfiguration(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	t.Run("ignored root workspace", func(t *testing.T) {
		checkout := createReleaseRepository(t, root)
		tagReleaseRepository(t, checkout)
		writeIgnoredFile(t, checkout, "go.work", []byte("go 1.27.2\nuse .\n"))
		assertReleaseRefused(t, checkout, "release refuses ignored release inputs")
	})
	t.Run("caller workspace and Go environment", func(t *testing.T) {
		checkout := createReleaseRepository(t, root)
		tagReleaseRepository(t, checkout)
		baseline, isolated := t.TempDir(), t.TempDir()
		command := releaseCommand(t, checkout, baseline)
		command.Env = append(command.Env, "GOWORK=off", "GOENV=off")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("baseline release: %v\n%s", err, output)
		}
		// The caller's environment file selects a higher AMD64 level and a FIPS build; the caller's
		// workspace covers the checkout. The go command that launches the build honours both, and the
		// build commands must still produce the baseline bytes.
		configuration := filepath.Join(t.TempDir(), "go-env")
		if err := os.WriteFile(configuration, []byte("GOAMD64=v3\nGOFIPS140=v1.0.0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		goModule, err := os.ReadFile(filepath.Join(checkout, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		goLine := regexp.MustCompile(`(?m)^go [0-9.]+`).FindString(string(goModule))
		workspace := filepath.Join(t.TempDir(), "go.work")
		if err := os.WriteFile(workspace, []byte(goLine+"\n\nuse "+filepath.ToSlash(checkout)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		command = releaseCommand(t, checkout, isolated)
		command.Env = append(command.Env, "GOWORK="+workspace, "GOENV="+configuration)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("caller-isolated release: %v\n%s", err, output)
		}
		assertReleaseArtifactsEqual(t, baseline, isolated)
	})
}

// TestReleaseIsolatesFIPSConfiguration proves caller FIPS settings cannot alter either executable's bytes through the actual release build command for the same clean tagged commit.
func TestReleaseIsolatesFIPSConfiguration(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	baseline, isolated := t.TempDir(), t.TempDir()
	command := releaseCommand(t, checkout, baseline)
	command.Env = append(command.Env, "GOFIPS140=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("baseline release: %v\n%s", err, output)
	}
	command = releaseCommand(t, checkout, isolated)
	command.Env = append(command.Env, "GOFIPS140=v1.0.0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("caller FIPS release: %v\n%s", err, output)
	}
	assertReleaseArtifactsEqual(t, baseline, isolated)
}

// writeIgnoredFile adds an ignored fixture file and verifies the release checkout otherwise stays clean.
func writeIgnoredFile(t *testing.T, checkout, relative string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(checkout, ".git", "info", "exclude"), []byte(filepath.Base(relative)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(checkout, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "check-ignore", "--quiet", relative)
	if status := gitOutput(t, checkout, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("ignored-file fixture must otherwise be clean: %s", status)
	}
}

// TestReleaseRefusesTrackedChanges proves a tracked edit in a tagged temporary worktree is refused through the actual release build command before outputs are built.
func TestReleaseRefusesTrackedChanges(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	file, err := os.OpenFile(filepath.Join(checkout, "go.mod"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n// tracked edit\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertReleaseRefused(t, checkout, "release requires a clean Git worktree")
}

// TestReleaseRefusesMissingGit proves a tagged temporary worktree with Git absent from PATH gets a clear prerequisite refusal through the actual release build command before outputs are built.
func TestReleaseRefusesMissingGit(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	programs := t.TempDir()
	for _, name := range []string{"go"} {
		program, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(program, filepath.Join(programs, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", programs)
	assertReleaseRefused(t, checkout, "release requires git to verify its worktree")
}

// tagReleaseRepository creates the annotated version tag used by release integration tests.
func tagReleaseRepository(t *testing.T, directory string) {
	t.Helper()
	runGit(t, directory, "tag", "-a", "v"+releasetest.ReleaseVersion, "-m", "release fixture")
}

// cloneReleaseRepository creates an independent release fixture clone with automatic CRLF conversion enabled.
func cloneReleaseRepository(t *testing.T, source string) string {
	t.Helper()
	checkout := t.TempDir()
	runGit(t, t.TempDir(), "clone", "--quiet", "--no-hardlinks", "-c", "core.autocrlf=true", source, checkout)
	return checkout
}

// createReleaseRepository copies release inputs into an isolated committed Git repository.
func createReleaseRepository(t *testing.T, root string) string {
	t.Helper()
	checkout := t.TempDir()
	releasetest.CopyRepositorySources(t, root, checkout)
	if err := image.WriteContext(os.DirFS(filepath.Join(root, "image", "context")), filepath.Join(checkout, "image", "context")); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "init", "--quiet")
	runGit(t, checkout, "config", "user.name", "Release Test")
	runGit(t, checkout, "config", "user.email", "release-test@example.invalid")
	runGit(t, checkout, "config", "core.autocrlf", "false")
	// Automatic garbage collection can run detached after a command returns and still write into the
	// fixture when the test removes it, which fails the cleanup of t.TempDir; the fixture never needs it.
	runGit(t, checkout, "config", "gc.auto", "0")
	runGit(t, checkout, "config", "gc.autoDetach", "false")
	runGit(t, checkout, "add", ".")
	runGit(t, checkout, "commit", "--quiet", "-m", "test(release): prepare source fixture")
	return checkout
}

// runGit executes a Git fixture command and fails the test on error.
func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	gitOutput(t, directory, args...)
}

// gitOutput executes Git in a fixture repository and returns trimmed combined output.
func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// assertReleaseRefused checks the release build command reports the expected refusal and produces no release artifacts.
func assertReleaseRefused(t *testing.T, checkout, diagnostic string) {
	t.Helper()
	outputDirectory := t.TempDir()
	command := releaseCommand(t, checkout, outputDirectory)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), diagnostic) {
		t.Fatalf("release error=%v output=%q; want refusal containing %q", err, output, diagnostic)
	}
	for _, name := range releaseArtifacts {
		if _, err := os.Stat(filepath.Join(outputDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("refused release created artifact %s", name)
		}
	}
}

// TestReleaseVersionSymbolAcceptsCRLFMetadata proves the Go metadata reader and actual release script use the same canonical linker target from CRLF metadata through their build interfaces.
func TestReleaseVersionSymbolAcceptsCRLFMetadata(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	metadata, err := os.ReadFile(filepath.Join(root, "scripts", "version-symbol.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantSymbol := strings.TrimSuffix(string(metadata), "\n")
	baseline := createReleaseRepository(t, root)
	tagReleaseRepository(t, baseline)
	crlfCheckout := cloneReleaseRepository(t, baseline)
	rewriteReleaseInputsToCRLF(t, crlfCheckout)
	gotSymbol, err := release.VersionSymbol(os.DirFS(crlfCheckout))
	if err != nil || gotSymbol != wantSymbol {
		t.Fatalf("metadata symbol = %q, error=%v; want %q", gotSymbol, err, wantSymbol)
	}
	reader := exec.Command("go", "run", "./internal/release/cmd/version-symbol")
	setReleaseTestDirectoryAndEnvironment(reader, crlfCheckout)
	symbolOutput, err := reader.CombinedOutput()
	if err != nil || string(symbolOutput) != gotSymbol+"\n" {
		t.Fatalf("build command symbol = %q, error=%v; want Go reader symbol %q", symbolOutput, err, gotSymbol+"\n")
	}
	outputDirectory := t.TempDir()
	buildRelease(t, crlfCheckout, outputDirectory)
	assertReleaseVersion(t, outputDirectory)
}

// releaseRepositoryOrSkip returns the repository for Linux release tests and skips every other host.
func releaseRepositoryOrSkip(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("release build integration requires a Linux build host")
	}
	return repositoryRoot(t)
}

// repositoryRoot resolves the repository root from the scripts test directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
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

// setReleaseTestDirectoryAndEnvironment selects a subprocess working directory and offline release test environment.
func setReleaseTestDirectoryAndEnvironment(command *exec.Cmd, directory string) {
	command.Dir = directory
	command.Env = releaseTestEnvironment()
}

// assertReleaseVersion checks the host-native release executable reports the prepared version.
func assertReleaseVersion(t *testing.T, directory string) {
	t.Helper()
	artifact := linuxArtifact
	if runtime.GOOS == "windows" {
		artifact = windowsArtifact
	}
	command := exec.Command(filepath.Join(directory, artifact), "version")
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedPreparedVersion(t, releasetest.ReleaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("release version: error=%v output=%q; want %q", err, output, want)
	}
}

// releaseCommand creates the actual release build command for the prepared test version, run from a checkout's own sources with the offline test environment.
func releaseCommand(t *testing.T, root, output string) *exec.Cmd {
	t.Helper()
	return releaseCommandForVersion(t, root, releasetest.ReleaseVersion, output)
}

// releaseCommandForVersion creates the actual release build command for one version, compiled from and run in the checkout with the offline test environment.
func releaseCommandForVersion(t *testing.T, root, version, output string) *exec.Cmd {
	t.Helper()
	command := exec.Command("go", "run", "./internal/release/cmd/build-release", version, output)
	setReleaseTestDirectoryAndEnvironment(command, root)
	return command
}

// buildRelease runs the actual release build command against a fixture checkout and fails the test on error.
func buildRelease(t *testing.T, root, output string) {
	t.Helper()
	command := releaseCommand(t, root, output)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
}

// assertReleaseArtifactsEqual proves executables, checksums, the npm package, and the NuGet command package match the baseline byte for byte.
func assertReleaseArtifactsEqual(t *testing.T, baseline string, directories ...string) {
	t.Helper()
	for _, name := range releaseArtifacts {
		want, err := os.ReadFile(filepath.Join(baseline, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, directory := range directories {
			got, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs between release outputs %s and %s", name, baseline, directory)
			}
		}
	}
}

// checkoutNeedsLF reports fixture files that stay at LF while release inputs switch to CRLF: the checkout attributes.
func checkoutNeedsLF(name string) bool {
	return name == ".gitattributes"
}

// rewriteReleaseInputsToCRLF rewrites selected release inputs and context text, preserving binaries and keeping the checkout attributes at LF.
func rewriteReleaseInputsToCRLF(t *testing.T, checkout string) {
	t.Helper()
	countGo := 0
	err := filepath.WalkDir(checkout, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(checkout, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relative = filepath.ToSlash(relative)
		if checkoutNeedsLF(relative) || (!releasetest.IsReleaseSource(relative) && !strings.HasPrefix(relative, "image/context/")) {
			return nil
		}
		if err := releasetest.RewriteFileToCRLF(checkout, relative); err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".go") {
			if !bytes.Contains(data, []byte("\r\n")) {
				return fmt.Errorf("Go source %s has no CRLF line endings", relative)
			}
			countGo++
		}
		if relative == "go.mod" && !bytes.Contains(data, []byte("\r\n")) {
			return fmt.Errorf("go.mod has no CRLF line endings")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if countGo == 0 {
		t.Fatal("CRLF fixture contains no Go sources")
	}
	// Refresh only the fixture index so forced-LF attributes recognize normalized CRLF bytes; HEAD and tag stay unchanged, and no staged changes are allowed.
	runGit(t, checkout, "add", "--renormalize", ".")
	if status := gitOutput(t, checkout, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("CRLF fixture must remain Git-clean: %s", status)
	}
}
