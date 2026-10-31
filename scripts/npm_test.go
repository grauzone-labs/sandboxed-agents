package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestNpmPackageLifecycle proves the release command produces an npm tarball that installs locally and globally, runs, protects launchers as workspace binds, updates, and uninstalls through npm without Podman, SSH, or host-data changes.
func TestNpmPackageLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("npm executable lifecycle requires Linux amd64 or Windows amd64")
	}
	root := repositoryRoot(t)
	// Prepare the release expectation before this test redirects HOME to its installation fixture.
	_ = releasetest.ExpectedPreparedVersion(t, releasetest.ReleaseVersion)
	checkout := createReleaseRepository(t, root)
	tagReleaseRepository(t, checkout)
	output := t.TempDir()
	buildRelease(t, checkout, output)
	archive := filepath.Join(output, "sandboxed-agents-"+releasetest.ReleaseVersion+".tgz")
	files := readNpmArchive(t, archive)
	for _, name := range []string{"package.json", "launcher.cjs", "verify.cjs", "SHA256SUMS", linuxArtifact, windowsArtifact} {
		if _, ok := files[name]; !ok {
			t.Errorf("npm package missing %s", name)
		}
	}
	var manifest struct {
		Name, Version string
		OS, CPU       []string
		Repository    struct {
			URL string `json:"url"`
		}
		Bin map[string]string
	}
	if err := json.Unmarshal(files["package.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "sandboxed-agents" || manifest.Version != releasetest.ReleaseVersion || manifest.Repository.URL != "https://github.com/grauzone-labs/sandboxed-agents.git" || manifest.Bin["sandboxed-agents"] != "launcher.cjs" {
		t.Fatalf("unexpected npm metadata: %+v", manifest)
	}
	if !reflect.DeepEqual(manifest.OS, []string{"linux", "win32"}) || !reflect.DeepEqual(manifest.CPU, []string{"x64"}) {
		t.Fatalf("npm platform metadata: os=%q cpu=%q; want linux/win32 and x64", manifest.OS, manifest.CPU)
	}
	const upgradeVersion = "0.0.31-test"
	runGit(t, checkout, "tag", "v"+upgradeVersion)
	upgradeOutput := t.TempDir()
	upgrade := releaseCommandForVersion(t, checkout, upgradeVersion, upgradeOutput)
	if result, err := upgrade.CombinedOutput(); err != nil {
		t.Fatalf("upgrade release build: %v\n%s", err, result)
	}
	upgradeArchive := filepath.Join(upgradeOutput, "sandboxed-agents-"+upgradeVersion+".tgz")
	fake := filepath.Join(t.TempDir(), "fake")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	build := exec.Command("go", "build", "-o", fake, "./internal/testutil/testdata/fake.go")
	setReleaseTestDirectoryAndEnvironment(build, root)
	if result, err := build.CombinedOutput(); err != nil {
		t.Fatalf("process fakes: %v\n%s", err, result)
	}
	fakes := testutil.Install(t, fake)
	t.Setenv("PATH", fakes.Dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_PODMAN_LOG", fakes.PodmanLog)
	t.Setenv("TEST_SSH_LOG", fakes.SSHLog)
	t.Setenv("TEST_PODMAN_EXIT", "91")
	t.Setenv("TEST_SSH_EXIT", "91")
	state, home := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("LOCALAPPDATA", state)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{filepath.Join(state, "sandboxed-agents", "group-default", "sentinel"), filepath.Join(home, ".ssh", "sentinel")} {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("host data must survive\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, global := range []bool{true, false} {
		t.Run(fmt.Sprintf("global=%t", global), func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "prefix with spaces")
			if err := os.MkdirAll(prefix, 0700); err != nil {
				t.Fatal(err)
			}
			install := []string{"install"}
			remove := []string{"uninstall"}
			packagePath, commandPath := filepath.Join(prefix, "node_modules", "sandboxed-agents"), filepath.Join(prefix, "node_modules", ".bin", "sandboxed-agents")
			if runtime.GOOS == "windows" {
				commandPath += ".cmd"
			}
			if global {
				install, remove = append(install, "--global"), append(remove, "--global")
				packagePath, commandPath = npmPackagePath(prefix), npmCommandPath(prefix)
			}
			for _, release := range []struct{ archive, version string }{{archive, releasetest.ReleaseVersion}, {upgradeArchive, upgradeVersion}} {
				runNpm(t, prefix, append(install, release.archive)...)
				command := npmShimCommand(commandPath, "version")
				result, err := command.CombinedOutput()
				if want := releasetest.ExpectedPreparedVersion(t, release.version); err != nil || string(result) != want {
					t.Fatalf("installed npm version: error=%v output=%q; want %q", err, result, want)
				}
				metadata, err := os.ReadFile(filepath.Join(packagePath, "package.json"))
				if err != nil {
					t.Fatal(err)
				}
				var installed struct{ Version string }
				if err := json.Unmarshal(metadata, &installed); err != nil {
					t.Fatal(err)
				}
				if installed.Version != release.version {
					t.Fatalf("installed npm package version=%q; want %q", installed.Version, release.version)
				}
			}
			launcher := filepath.Join(packagePath, "launcher.cjs")
			for _, workspace := range []string{packagePath, filepath.Dir(commandPath)} {
				assertInstalledNpmWorkspaceProtected(t, launcher, workspace)
			}
			t.Run("external launch link", func(t *testing.T) {
				link := filepath.Join(t.TempDir(), "launch-link.cjs")
				if err := os.Symlink(launcher, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				assertInstalledNpmWorkspaceProtected(t, link, filepath.Dir(link))
			})
			runNpm(t, prefix, append(remove, "sandboxed-agents")...)
			if _, err := os.Stat(commandPath); !os.IsNotExist(err) {
				t.Fatalf("npm uninstall left command: %v", err)
			}
		})
	}
	for _, log := range []string{fakes.PodmanLog, fakes.SSHLog} {
		if calls := testutil.Invocations(t, log); len(calls) != 0 {
			t.Errorf("npm lifecycle made external calls: %v", calls)
		}
	}
	for _, directory := range []string{filepath.Join(state, "sandboxed-agents", "group-default"), filepath.Join(home, ".ssh")} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
			t.Fatalf("npm lifecycle changed host data: entries=%v error=%v", entries, err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "sentinel"))
		if err != nil || string(data) != "host data must survive\n" {
			t.Fatalf("npm lifecycle changed host data: %q error=%v", data, err)
		}
	}
}

// assertInstalledNpmWorkspaceProtected checks an installed launcher refuses a protected workspace before host operations.
func assertInstalledNpmWorkspaceProtected(t *testing.T, launcher, workspace string) {
	t.Helper()
	// Invoke Node directly: the npm shim directory is deliberately absent from PATH.
	command := exec.Command("node", launcher, "up", "agent01", workspace)
	result, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(result), "step 1:") || !strings.Contains(string(result), "protected host path") {
		t.Fatalf("installed npm workspace: error=%v output=%s; want protected-path refusal at step 1", err, result)
	}
}

// TestNpmLauncherPreservesProcessBoundary proves the installed launcher forwards exact arguments, stdin, stdout, stderr, and exit status and supplies existing launcher and shim paths to its executable child.
func TestNpmLauncherPreservesProcessBoundary(t *testing.T) {
	archive, _ := fixtureNpmArchive(t)
	prefix := filepath.Join(t.TempDir(), "prefix with spaces")
	if err := os.MkdirAll(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	runNpm(t, prefix, "install", "--global", archive)
	wantArgs := []string{"two words", "semi;colon", "$(literal)", "quote\"value", ""}
	command := installedNpmCommand(t, prefix, wantArgs...)
	if runtime.GOOS == "windows" {
		command = exec.Command("node", append([]string{filepath.Join(npmPackagePath(prefix), "launcher.cjs")}, wantArgs...)...)
	}
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installed launcher: %v\n%s", err, result)
	}
	var child struct {
		Args      []string
		Launchers string
	}
	if err := json.Unmarshal(result, &child); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.Args, wantArgs) {
		t.Fatalf("child args=%q; want %q", child.Args, wantArgs)
	}
	var launchers []string
	if err := json.Unmarshal([]byte(child.Launchers), &launchers); err != nil {
		t.Fatalf("launcher paths are not JSON: %q: %v", child.Launchers, err)
	}
	wantLaunchers := []string{filepath.Join(npmPackagePath(prefix), "launcher.cjs"), npmCommandPath(prefix)}
	if runtime.GOOS == "windows" {
		wantLaunchers = append(wantLaunchers, filepath.Join(prefix, "sandboxed-agents"), filepath.Join(prefix, "sandboxed-agents.ps1"))
	}
	for _, want := range wantLaunchers {
		found := false
		for _, got := range launchers {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("child launcher paths=%q; missing %q", launchers, want)
		}
	}
	for _, name := range launchers {
		if !filepath.IsAbs(name) {
			t.Errorf("launcher path is relative: %q", name)
		}
		if _, err := os.Stat(name); err != nil {
			t.Errorf("launcher path does not exist: %q: %v", name, err)
		}
	}
	command = installedNpmCommand(t, prefix, "forward")
	command.Stdin = strings.NewReader("input with spaces\n")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 || stdout.String() != "stdin:input with spaces\n" || stderr.String() != "child stderr\n" {
		t.Fatalf("forwarding: error=%v stdout=%q stderr=%q; want status 23 and child streams", err, stdout.String(), stderr.String())
	}
}

// TestNpmRejectsUnsupportedPlatforms proves npm installation and the installed launcher clearly reject macOS and ARM64 through a Node process-platform boundary fixture.
func TestNpmRejectsUnsupportedPlatforms(t *testing.T) {
	archive, _ := fixtureNpmArchive(t)
	prefix := t.TempDir()
	runNpm(t, prefix, "install", "--global", archive)
	for _, platform := range []struct{ os, arch string }{{"darwin", "x64"}, {"linux", "arm64"}, {"win32", "arm64"}} {
		t.Run(platform.os+"-"+platform.arch, func(t *testing.T) {
			preload := filepath.Join(t.TempDir(), "platform.cjs")
			script := fmt.Sprintf("if (/[/\\\\]((verify|launcher)\\.cjs|sandboxed-agents)$/.test(process.argv[1] || '')) { Object.defineProperty(process, 'platform', {value:%q}); Object.defineProperty(process, 'arch', {value:%q}); }\n", platform.os, platform.arch)
			if err := os.WriteFile(preload, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			unsupported := "unsupported platform " + platform.os + "-" + platform.arch
			for _, command := range []*exec.Cmd{npmCommand(t, t.TempDir(), "install", "--global", archive), installedNpmCommand(t, prefix)} {
				base := command.Env
				if base == nil {
					base = os.Environ()
				}
				command.Env = append(base, "NODE_OPTIONS=--require="+strconv.Quote(preload))
				result, err := command.CombinedOutput()
				if err == nil || !strings.Contains(string(result), unsupported) {
					t.Fatalf("unsupported platform: error=%v output=%s; want %q", err, result, unsupported)
				}
			}
		})
	}
}

// TestNpmMetadataRejectsUnsupportedPlatformsWithoutScripts proves npm itself refuses ARM64 on either supported OS and macOS on Linux with lifecycle scripts disabled through packed-tarball installation on a simulated Node host.
func TestNpmMetadataRejectsUnsupportedPlatformsWithoutScripts(t *testing.T) {
	archive, _ := fixtureNpmArchive(t)
	nodeOS := runtime.GOOS
	if nodeOS == "windows" {
		nodeOS = "win32"
	}
	platforms := []struct{ os, arch string }{{nodeOS, "arm64"}}
	// Spoofing Unix as Windows (or Windows as Unix) changes npm path parsing before installation.
	if runtime.GOOS == "linux" {
		platforms = append(platforms, struct{ os, arch string }{"darwin", "x64"})
	}
	for _, platform := range platforms {
		t.Run(platform.os+"-"+platform.arch, func(t *testing.T) {
			preload := filepath.Join(t.TempDir(), "npm-platform.cjs")
			// npm-install-checks reads these Node host values directly for non-optional packages.
			script := fmt.Sprintf("Object.defineProperty(process, 'platform', {value:%q}); Object.defineProperty(process, 'arch', {value:%q});\n", platform.os, platform.arch)
			if err := os.WriteFile(preload, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			command := npmCommand(t, t.TempDir(), "install", "--global", "--ignore-scripts", archive)
			command.Env = testutil.Env(command.Env, map[string]string{"NODE_OPTIONS": "--require=" + strconv.Quote(preload)})
			result, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(result), "EBADPLATFORM") || !strings.Contains(string(result), "Unsupported platform") {
				t.Fatalf("unsupported npm host with scripts disabled: error=%v output=%s; want EBADPLATFORM refusal", err, result)
			}
		})
	}
}

// TestNpmRejectsTamperedBinaries proves npm installation and every installed command invocation reject executable bytes that differ from the packaged SHA256SUMS manifest.
func TestNpmRejectsTamperedBinaries(t *testing.T) {
	for _, artifact := range []string{linuxArtifact, windowsArtifact} {
		t.Run("installation/"+artifact, func(t *testing.T) {
			_, artifacts := fixtureNpmArchive(t)
			appendNpmTamper(t, filepath.Join(artifacts, artifact))
			archive := packNpmFixture(t, artifacts)
			prefix := t.TempDir()
			result, err := npmCommand(t, prefix, "install", "--global", archive).CombinedOutput()
			if err == nil || !strings.Contains(string(result), "checksum mismatch") {
				t.Fatalf("tampered npm install: error=%v output=%s; want checksum refusal", err, result)
			}
		})
	}
	t.Run("every invocation", func(t *testing.T) {
		archive, _ := fixtureNpmArchive(t)
		prefix := t.TempDir()
		runNpm(t, prefix, "install", "--global", archive)
		appendNpmTamper(t, filepath.Join(npmPackagePath(prefix), npmHostArtifact()))
		for range 2 {
			result, err := installedNpmCommand(t, prefix).CombinedOutput()
			if err == nil || !strings.Contains(string(result), "checksum mismatch") {
				t.Fatalf("tampered installed command: error=%v output=%s; want checksum refusal", err, result)
			}
		}
	})
}

// fixtureNpmArchive builds a host-native fixture executable and packages it with checksums for npm installation tests.
func fixtureNpmArchive(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("npm fixture requires Linux amd64 or Windows amd64")
	}
	artifacts := t.TempDir()
	command := exec.Command("go", "build", "-o", filepath.Join(artifacts, npmHostArtifact()), "./scripts/testdata/npm-child.go")
	setReleaseTestDirectoryAndEnvironment(command, repositoryRoot(t))
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture child: %v\n%s", err, result)
	}
	other := linuxArtifact
	if runtime.GOOS == "linux" {
		other = windowsArtifact
	}
	if err := os.WriteFile(filepath.Join(artifacts, other), []byte("other host executable"), 0644); err != nil {
		t.Fatal(err)
	}
	var manifest strings.Builder
	for _, name := range []string{linuxArtifact, windowsArtifact} {
		data, err := os.ReadFile(filepath.Join(artifacts, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(data), name)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "SHA256SUMS"), []byte(manifest.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return packNpmFixture(t, artifacts), artifacts
}

// packNpmFixture runs the npm package builder against fixture release artifacts and returns the archive path.
func packNpmFixture(t *testing.T, artifacts string) string {
	t.Helper()
	root := repositoryRoot(t)
	command := exec.Command("go", "run", "./internal/npm/cmd/package", releasetest.ReleaseVersion, root, artifacts)
	setReleaseTestDirectoryAndEnvironment(command, root)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("npm package fixture: %v\n%s", err, result)
	}
	return filepath.Join(artifacts, "sandboxed-agents-"+releasetest.ReleaseVersion+".tgz")
}

// npmHostArtifact returns the release executable name for the current test host.
func npmHostArtifact() string {
	if runtime.GOOS == "windows" {
		return windowsArtifact
	}
	return linuxArtifact
}

// npmPackagePath returns the globally installed package directory under an npm prefix.
func npmPackagePath(prefix string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(prefix, "node_modules", "sandboxed-agents")
	}
	return filepath.Join(prefix, "lib", "node_modules", "sandboxed-agents")
}

// appendNpmTamper appends bytes to a fixture artifact to exercise integrity checks.
func appendNpmTamper(t *testing.T, name string) {
	t.Helper()
	file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("tampered"); err != nil {
		t.Fatal(err)
	}
}

// readNpmArchive reads regular package files from an npm archive and rejects unexpected entries.
func readNpmArchive(t *testing.T, name string) map[string][]byte {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := map[string][]byte{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(header.Name, "package/") || header.Typeflag != tar.TypeReg {
			t.Fatalf("unexpected npm entry %q", header.Name)
		}
		data, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(header.Name, "package/")] = data
	}
	return files
}

// npmCommand creates an offline npm command with isolated cache and configuration paths.
func npmCommand(t *testing.T, prefix string, args ...string) *exec.Cmd {
	t.Helper()
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal("npm package tests require Node.js and npm")
	}
	arguments := append([]string{"--prefix", prefix, "--cache", filepath.Join(prefix, "cache"), "--offline", "--no-audit", "--no-fund", "--update-notifier=false"}, args...)
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.Command("cmd", append([]string{"/d", "/c", "call", npm}, arguments...)...)
	} else {
		command = exec.Command(npm, arguments...)
	}
	command.Dir = prefix
	command.Env = append(os.Environ(), "npm_config_userconfig="+filepath.Join(prefix, "missing.npmrc"), "npm_config_globalconfig="+filepath.Join(prefix, "missing-global.npmrc"))
	return command
}

// runNpm executes the isolated npm command and fails the test on an unsuccessful exit.
func runNpm(t *testing.T, prefix string, args ...string) {
	t.Helper()
	if result, err := npmCommand(t, prefix, args...).CombinedOutput(); err != nil {
		t.Fatalf("npm %v: %v\n%s", args, err, result)
	}
}

// npmCommandPath returns the platform-specific installed npm shim path.
func npmCommandPath(prefix string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(prefix, "sandboxed-agents.cmd")
	}
	return filepath.Join(prefix, "bin", "sandboxed-agents")
}

// installedNpmCommand creates a command invoking the installed npm shim under the test prefix.
func installedNpmCommand(t *testing.T, prefix string, args ...string) *exec.Cmd {
	t.Helper()
	return npmShimCommand(npmCommandPath(prefix), args...)
}

// npmShimCommand creates a platform-appropriate command for an npm shim.
func npmShimCommand(name string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", append([]string{"/d", "/c", "call", name}, args...)...)
	}
	return exec.Command(name, args...)
}
