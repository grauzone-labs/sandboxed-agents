// Command build-release builds the release files of one version from the repository root: the
// static Linux and Windows AMD64 executables with the embedded Linux manager, SHA256SUMS, the npm
// package, and the NuGet command package. In a Git checkout it refuses to build unless the version
// tag points at HEAD, the worktree is clean, and no ignored file lies among the release inputs. It
// requires the Go version of go.mod on PATH and isolates every Go command from the caller's
// workspace, environment file, build flags, and FIPS setting, so the same commit yields the same
// bytes on every host.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

const (
	versionNumber     = `(?:0|[1-9][0-9]*)`
	versionIdentifier = `(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)`
)

// versionPattern accepts X.Y.Z with optional SemVer prerelease identifiers and build metadata, the
// same syntax the NuGet package command accepts.
var versionPattern = regexp.MustCompile(`^` + versionNumber + `\.` + versionNumber + `\.` + versionNumber + `(?:-` + versionIdentifier + `(?:\.` + versionIdentifier + `)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// releaseInputs are the Git pathspecs of every input of a release build; an ignored file among them
// would enter the build without being part of the tagged commit.
var releaseInputs = []string{":(glob)image/context/**", "go.mod", "go.sum", "go.work", ".gitattributes", ":(glob)**/*.go", "scripts/version-symbol.txt", "packages/nuget/SandboxedAgents.nuspec", ":(glob)packages/nuget/tools/**", ":(glob)packages/npm/**"}

// goEnvironment fixes the Go environment of every build command: the local toolchain only, no
// cgo, no caller flags or experiments, the baseline AMD64 level, and no workspace, environment
// file, or FIPS mode from the caller.
var goEnvironment = map[string]string{"GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOFLAGS": "", "GOEXPERIMENT": "", "GOAMD64": "v1", "GOWORK": "off", "GOENV": "off", "GOFIPS140": "off"}

// main validates the arguments, builds the release, and exits with the result.
func main() {
	if len(os.Args) != 3 || !versionPattern.MatchString(os.Args[1]) {
		fmt.Fprintf(os.Stderr, "usage: %s VERSION OUTPUT_DIRECTORY (run from the repository root)\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	if err := build(os.Args[1], os.Args[2], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// build verifies the checkout and the toolchain, then builds every release file of version into
// the output directory, creating it as needed. The current directory must be the repository root.
func build(version, outputArgument string, stdout, stderr io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return errors.New("release must run from the repository root, which holds go.mod")
	}
	if err := os.MkdirAll(outputArgument, 0777); err != nil {
		return err
	}
	output, err := filepath.Abs(outputArgument)
	if err != nil {
		return err
	}
	if err := verifyWorktree(root, version, stderr); err != nil {
		return err
	}
	environment := withEnvironment(os.Environ(), goEnvironment)
	requiredGo, err := requiredGoVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	goVersion, err := goEnv(root, environment, "GOVERSION")
	if err != nil {
		return err
	}
	if goVersion != requiredGo {
		return fmt.Errorf("release requires %s on PATH", requiredGo)
	}
	hostOS, err := goEnv(root, environment, "GOHOSTOS")
	if err != nil {
		return err
	}
	hostArch, err := goEnv(root, environment, "GOHOSTARCH")
	if err != nil {
		return err
	}
	hostEnvironment := withEnvironment(environment, map[string]string{"GOOS": hostOS, "GOARCH": hostArch})
	overlay, err := os.MkdirTemp("", "sandboxed-agents-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(overlay)
	if err := goCommand(root, hostEnvironment, stdout, stderr, "run", "./image/cmd/overlay", overlay); err != nil {
		return err
	}
	versionSymbol, err := release.VersionSymbol(os.DirFS(root))
	if err != nil {
		return err
	}
	for _, target := range []string{"linux", "windows"} {
		suffix := ""
		if target == "windows" {
			suffix = ".exe"
		}
		targetEnvironment := withEnvironment(environment, map[string]string{"GOOS": target, "GOARCH": "amd64"})
		if err := goCommand(root, targetEnvironment, stdout, stderr, "build", "-overlay", filepath.Join(overlay, "overlay.json"), "-trimpath", "-buildvcs=false",
			"-ldflags", "-s -w -buildid= -X "+versionSymbol+"="+version, "-o", filepath.Join(output, "sandboxed-agents-"+target+"-amd64"+suffix), "./cmd/sandboxed-agents"); err != nil {
			return err
		}
	}
	for _, command := range []string{"./internal/nuget/cmd/package", "./internal/npm/cmd/package"} {
		if err := goCommand(root, hostEnvironment, stdout, stderr, "run", command, version, root, output); err != nil {
			return err
		}
	}
	return nil
}

// verifyWorktree refuses a Git checkout whose version tag is not at HEAD, which has tracked or
// untracked changes, or which holds ignored files among the release inputs. A directory without a
// Git entry and without Git on PATH is built as it is; a Git entry that Git cannot inspect, or
// Git missing while a Git entry exists, is refused.
func verifyWorktree(root, version string, stderr io.Writer) error {
	hasGitEntry := false
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		hasGitEntry = true
	}
	if _, err := exec.LookPath("git"); err != nil {
		if hasGitEntry {
			return errors.New("release requires git to verify its worktree")
		}
		return nil
	}
	inside, err := gitOutput(root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if hasGitEntry {
			return errors.New("release cannot inspect its Git worktree")
		}
		return nil
	}
	if inside != "true" {
		return nil
	}
	tag := "refs/tags/v" + version
	tagCommit, err := gitOutput(root, "rev-parse", "--verify", tag+"^{commit}")
	if err != nil {
		return fmt.Errorf("release requires tag %s at HEAD", tag)
	}
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("release cannot read HEAD: %w", err)
	}
	if tagCommit != head {
		return fmt.Errorf("release tag %s does not point to HEAD", tag)
	}
	status, err := gitOutput(root, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("release cannot read the worktree status: %w", err)
	}
	if status != "" {
		return errors.New("release requires a clean Git worktree (tracked and untracked files)")
	}
	ignored, err := gitOutput(root, append([]string{"ls-files", "--others", "--ignored", "--exclude-standard", "--"}, releaseInputs...)...)
	if err != nil {
		return fmt.Errorf("release cannot list ignored release inputs: %w", err)
	}
	if ignored != "" {
		return fmt.Errorf("release refuses ignored release inputs:\n%s", ignored)
	}
	return nil
}

// gitOutput runs Git in the root and returns its trimmed standard output.
func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// requiredGoVersion reads the go directive of go.mod, tolerating CRLF, and returns it as goX.Y.Z.
func requiredGoVersion(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimRight(scanner.Text(), "\r"))
		if len(fields) == 2 && fields[0] == "go" {
			return "go" + fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("go.mod names no Go version")
}

// goEnv returns one value of the Go environment as the go command on PATH reports it.
func goEnv(root string, environment []string, name string) (string, error) {
	command := exec.Command("go", "env", name)
	command.Dir = root
	command.Env = environment
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w\n%s", name, err, stderr.String())
	}
	return strings.TrimSpace(string(output)), nil
}

// goCommand runs the go command on PATH in the root with the given environment and passes its
// output through.
func goCommand(root string, environment []string, stdout, stderr io.Writer, args ...string) error {
	command := exec.Command("go", args...)
	command.Dir = root
	command.Env = environment
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return nil
}

// withEnvironment replaces or adds variables in an environment, matching names exactly on Unix
// and without regard to case on Windows, as the Go command itself does.
func withEnvironment(base []string, values map[string]string) []string {
	caseInsensitive := runtime.GOOS == "windows"
	result := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		replaced := false
		for name := range values {
			if name == key || caseInsensitive && strings.EqualFold(name, key) {
				replaced = true
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}
