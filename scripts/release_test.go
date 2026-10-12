package scripts_test

import (
	"bytes"
	"debug/pe"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// These artifact names mirror scripts/release.sh's sandboxed-agents-$target-amd64$suffix pattern.
const (
	linuxArtifact   = "sandboxed-agents-linux-amd64"
	windowsArtifact = "sandboxed-agents-windows-amd64.exe"
)

// TestReleaseBuildsAreReproducible proves the same tagged commit yields identical Linux and Windows executables from LF and CRLF assets, Go sources, and metadata through the release script on Linux.
func TestReleaseBuildsAreReproducible(t *testing.T) {
	assertReproducibleRelease(t, releaseRepository(t))
}

// TestWindowsReleaseBuildsAreReproducible proves the same tagged commit yields identical executable bytes and a working Windows version command through explicitly configured Git for Windows Bash.
func TestWindowsReleaseBuildsAreReproducible(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows release integration requires Windows and configured Git for Windows Bash")
	}
	releaseBash(t)
	assertReproducibleRelease(t, repositoryRoot(t))
}

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

// TestReleaseRefusesMissingVersionTag proves an untagged commit is refused through the actual release script before outputs are built.
func TestReleaseRefusesMissingVersionTag(t *testing.T) {
	root := releaseRepository(t)
	checkout := createReleaseRepository(t, root)
	assertReleaseRefused(t, checkout, "release requires tag refs/tags/v"+releasetest.ReleaseVersion)
}

// TestReleaseRefusesVersionTagOnAnotherCommit proves a tag targeting a previous commit is refused through the actual release script before outputs are built.
func TestReleaseRefusesVersionTagOnAnotherCommit(t *testing.T) {
	root := releaseRepository(t)
	checkout := createReleaseRepository(t, root)
	tagReleaseRepository(t, checkout)
	runGit(t, checkout, "commit", "--quiet", "--allow-empty", "-m", "test(release): advance fixture HEAD")
	assertReleaseRefused(t, checkout, "release tag refs/tags/v"+releasetest.ReleaseVersion+" does not point to HEAD")
}

// TestReleaseRefusesUntrackedFiles proves an untracked file in a tagged temporary worktree is refused through the actual release script before outputs are built.
func TestReleaseRefusesUntrackedFiles(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepository(t))
	tagReleaseRepository(t, checkout)
	if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("extra input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertReleaseRefused(t, checkout, "release requires a clean Git worktree")
}

// TestReleaseRefusesTrackedChanges proves a tracked edit in a tagged temporary worktree is refused through the actual release script before outputs are built.
func TestReleaseRefusesTrackedChanges(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepository(t))
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

// TestReleaseRefusesMissingGit proves a tagged temporary worktree with Git absent from PATH gets a clear prerequisite refusal through the actual release script before outputs are built.
func TestReleaseRefusesMissingGit(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepository(t))
	tagReleaseRepository(t, checkout)
	programs := t.TempDir()
	for _, name := range []string{"bash", "go", "awk", "mkdir", "dirname", "mktemp", "rm"} {
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

func tagReleaseRepository(t *testing.T, directory string) {
	t.Helper()
	runGit(t, directory, "tag", "-a", "v"+releasetest.ReleaseVersion, "-m", "release fixture")
}

func cloneReleaseRepository(t *testing.T, source string) string {
	t.Helper()
	checkout := t.TempDir()
	runGit(t, t.TempDir(), "clone", "--quiet", "--no-hardlinks", "-c", "core.autocrlf=true", source, checkout)
	return checkout
}

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
	runGit(t, checkout, "add", ".")
	runGit(t, checkout, "commit", "--quiet", "-m", "test(release): prepare source fixture")
	return checkout
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	gitOutput(t, directory, args...)
}

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

func assertReleaseRefused(t *testing.T, checkout, diagnostic string) {
	t.Helper()
	outputDirectory := t.TempDir()
	command := releaseCommand(t, checkout, outputDirectory)
	command.Dir = checkout
	command.Env = releaseTestEnvironment()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), diagnostic) {
		t.Fatalf("release error=%v output=%q; want refusal containing %q", err, output, diagnostic)
	}
	for _, name := range []string{linuxArtifact, windowsArtifact} {
		if _, err := os.Stat(filepath.Join(outputDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("refused release created artifact %s", name)
		}
	}
}

// TestReleaseVersionSymbolAcceptsCRLFMetadata proves the Go metadata reader and actual release script use the same canonical linker target from CRLF metadata through their build interfaces.
func TestReleaseVersionSymbolAcceptsCRLFMetadata(t *testing.T) {
	root := releaseRepository(t)
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
	return repositoryRoot(t)
}

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

func assertReleaseVersion(t *testing.T, directory string) {
	t.Helper()
	artifact := linuxArtifact
	if runtime.GOOS == "windows" {
		artifact = windowsArtifact
	}
	command := exec.Command(filepath.Join(directory, artifact), "version")
	output, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releasetest.ReleaseVersion)
	if err != nil || string(output) != want {
		t.Fatalf("release version: error=%v output=%q; want %q", err, output, want)
	}
}

func releaseBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "bash"
	}
	configured := os.Getenv("SANDBOXED_AGENTS_RELEASE_BASH")
	if configured == "" {
		t.Fatal("SANDBOXED_AGENTS_RELEASE_BASH must name Git for Windows Bash on Windows")
	}
	if info, err := os.Stat(configured); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("SANDBOXED_AGENTS_RELEASE_BASH %q must be an existing Git for Windows Bash executable", configured)
	}
	return configured
}

func releaseCommand(t *testing.T, root, output string) *exec.Cmd {
	t.Helper()
	return exec.Command(releaseBash(t), filepath.ToSlash(filepath.Join(root, "scripts/release.sh")), releasetest.ReleaseVersion, filepath.ToSlash(output))
}

func buildRelease(t *testing.T, root, output string) {
	t.Helper()
	command := releaseCommand(t, root, output)
	command.Dir = root
	command.Env = releaseTestEnvironment()
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
}

// rewriteReleaseInputsToCRLF rewrites selected release inputs and context text, preserving binaries and keeping checkout attributes and the executable release script at LF.
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
		if relative == ".gitattributes" || relative == "scripts/release.sh" || !releasetest.IsReleaseSource(relative) && !strings.HasPrefix(relative, "image/context/") {
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
