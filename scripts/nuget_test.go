package scripts_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/releasetest"
)

// TestNuGetCommandScripts proves verified install, upgrade, removal, and raw user PATH preservation through the actual PowerShell command scripts and registry boundary.
func TestNuGetCommandScripts(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("NuGet command script tests require PowerShell 7 (pwsh)")
	}
	for _, scenario := range []string{"install", "upgrade", "checksum", "copied-checksum", "remove", "path", "default-no-path", "path-aliases", "path-errors", "relative-path", "empty-path", "preexisting-path", "owned-path", "path-during-copy", "path-registry-kind", "path-write-failure", "replacement-failure", "locked-upgrade", "windows-registry"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "locked-upgrade" && runtime.GOOS != "windows" {
				t.Skip("executable sharing-lock replacement behavior requires Windows")
			}
			if scenario == "windows-registry" && !hkcuIntegrationEnabled(runtime.GOOS, os.Getenv("SANDBOXED_AGENTS_TEST_HKCU")) {
				t.Skip("real user PATH registry integration requires Windows and SANDBOXED_AGENTS_TEST_HKCU=1")
			}
			command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join(repositoryRoot(t), "scripts", "test-nuget-command.ps1"), "-Scenario", scenario, "-Repository", repositoryRoot(t), "-TestDirectory", t.TempDir())
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("PowerShell %s: %v\n%s", scenario, err, output)
			}
		})
	}
}

func hkcuIntegrationEnabled(host, optIn string) bool {
	return host == "windows" && optIn == "1"
}

// TestHKCUIntegrationRequiresExplicitOptIn proves the test runner selects real user PATH integration only on Windows with the exact explicit opt-in value.
func TestHKCUIntegrationRequiresExplicitOptIn(t *testing.T) {
	for _, fixture := range []struct {
		host, optIn string
		want        bool
	}{
		{"windows", "", false},
		{"windows", "0", false},
		{"windows", "true", false},
		{"windows", "1", true},
		{"linux", "1", false},
	} {
		if got := hkcuIntegrationEnabled(fixture.host, fixture.optIn); got != fixture.want {
			t.Errorf("HKCU integration on %s with opt-in %q = %t; want %t", fixture.host, fixture.optIn, got, fixture.want)
		}
	}
}

// TestNuGetWindowsRegistryHarnessRequiresOptIn proves direct PowerShell harness invocation refuses without opt-in before creating fixtures or opening HKCU.
func TestNuGetWindowsRegistryHarnessRequiresOptIn(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("NuGet command script tests require PowerShell 7 (pwsh)")
	}
	t.Setenv("SANDBOXED_AGENTS_TEST_HKCU", "")
	fixtures := t.TempDir()
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", filepath.Join(repositoryRoot(t), "scripts", "test-nuget-command.ps1"), "-Scenario", "windows-registry", "-Repository", repositoryRoot(t), "-TestDirectory", fixtures)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "SANDBOXED_AGENTS_TEST_HKCU=1") {
		t.Fatalf("unguarded HKCU harness: error=%v output=%s", err, output)
	}
	entries, err := os.ReadDir(fixtures)
	if err != nil || len(entries) != 0 {
		t.Fatalf("HKCU harness created fixtures before opt-in: entries=%v error=%v", entries, err)
	}
}

// TestReleaseRefusesIgnoredNuGetInputs proves ignored NuGet tooling cannot enter a tagged release through the release script's clean-input guard.
func TestReleaseRefusesIgnoredNuGetInputs(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	tagReleaseRepository(t, checkout)
	writeIgnoredFile(t, checkout, "packages/nuget/tools/ignored-helper.ps1", []byte("Write-Output 'ignored input'\n"))
	assertReleaseRefused(t, checkout, "release refuses ignored release inputs")
}

// TestReleaseRefusesMalformedVersionsBeforeArtifacts proves release.sh rejects versions outside the NuGet version syntax before creating an output directory or executable.
func TestReleaseRefusesMalformedVersionsBeforeArtifacts(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	runGit(t, checkout, "tag", "v1.0")
	for _, version := range []string{"1.0", "1.2.3-preview..1", "1.2.3+build..5", "01.2.3", "1.02.3", "1.2.03", "1.2.3-preview.01", "1.2.3-01"} {
		t.Run(version, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "release")
			command := exec.Command(releaseBash(t), filepath.ToSlash(filepath.Join(checkout, "scripts/release.sh")), version, filepath.ToSlash(output))
			setReleaseTestDirectoryAndEnvironment(command, checkout)
			result, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("malformed version was accepted: %s", result)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("malformed version wrote its release output directory: error=%v; output=%s", err, result)
			}
		})
	}
}

// TestNuGetCommandPackagePermissions proves the package command emits a NuGet command package readable by its owner, group, and others through the output file's Unix permissions.
func TestNuGetCommandPackagePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NuGet command package Unix permissions require a Unix host")
	}
	root := repositoryRoot(t)
	output := t.TempDir()
	for _, name := range []string{linuxArtifact, windowsArtifact} {
		if err := os.WriteFile(filepath.Join(output, name), []byte("release fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "run", "./internal/nuget/cmd/package", "1.2.3", root, output)
	setReleaseTestDirectoryAndEnvironment(command, root)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package command: %v\n%s", err, result)
	}
	info, err := os.Stat(filepath.Join(output, "SandboxedAgents.1.2.3.nupkg"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Fatalf("NuGet command package permissions = %#o; want 0644", got)
	}
}

// TestNuGetPackageRejectsNumericLeadingZeros proves the package command refuses numeric leading zeros rejected by NuGet before writing package artifacts.
func TestNuGetPackageRejectsNumericLeadingZeros(t *testing.T) {
	root := repositoryRoot(t)
	for _, version := range []string{"01.2.3", "1.02.3", "1.2.03", "1.2.3-preview.01", "1.2.3-01"} {
		t.Run(version, func(t *testing.T) {
			output := t.TempDir()
			for _, name := range []string{linuxArtifact, windowsArtifact} {
				if err := os.WriteFile(filepath.Join(output, name), []byte("release fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("go", "run", "./internal/nuget/cmd/package", version, root, output)
			setReleaseTestDirectoryAndEnvironment(command, root)
			result, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(result), "must be X.Y.Z") {
				t.Fatalf("NuGet-invalid version accepted by package command: error=%v output=%s", err, result)
			}
			entries, err := os.ReadDir(output)
			if err != nil || len(entries) != 2 {
				t.Fatalf("NuGet-invalid version wrote package artifacts: entries=%v error=%v", entries, err)
			}
		})
	}
}

// TestNuGetPackageFilenameOmitsBuildMetadata proves release.sh names the package with its normalized version while preserving build metadata in the NuGet manifest.
func TestNuGetPackageFilenameOmitsBuildMetadata(t *testing.T) {
	checkout := createReleaseRepository(t, releaseRepositoryOrSkip(t))
	version := "1.2.3-preview.1+build.01"
	runGit(t, checkout, "tag", "v"+version)
	output := t.TempDir()
	command := exec.Command(releaseBash(t), filepath.ToSlash(filepath.Join(checkout, "scripts/release.sh")), version, filepath.ToSlash(output))
	setReleaseTestDirectoryAndEnvironment(command, checkout)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release build: %v\n%s", err, result)
	}
	archive, err := zip.OpenReader(filepath.Join(output, "SandboxedAgents.1.2.3-preview.1.nupkg"))
	if err != nil {
		t.Fatalf("normalized NuGet package filename missing: %v", err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "SandboxedAgents.nuspec" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(manifest), "<version>1.2.3-preview.1+build.01</version>") {
			t.Fatalf("NuGet manifest lost version build metadata: %s", manifest)
		}
		return
	}
	t.Fatal("NuGet package has no manifest")
}

// TestNuGetCommandPackage proves the release script produces a NuGet command package with repository metadata, the Windows executable, PowerShell scripts, and matching release checksums.
func TestNuGetCommandPackage(t *testing.T) {
	root := releaseRepositoryOrSkip(t)
	checkout := createReleaseRepository(t, root)
	tagReleaseRepository(t, checkout)
	output := t.TempDir()
	buildRelease(t, checkout, output)
	packageFile := filepath.Join(output, "SandboxedAgents."+releasetest.ReleaseVersion+".nupkg")
	archive, err := zip.OpenReader(packageFile)
	if err != nil {
		t.Fatalf("release did not produce a NuGet command package: %v", err)
	}
	defer archive.Close()
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "SandboxedAgents.nuspec", "tools/install-command.ps1", "tools/remove-command.ps1", "tools/command-environment.ps1", "tools/" + windowsArtifact, "tools/SHA256SUMS"} {
		if _, ok := files[name]; !ok {
			t.Errorf("NuGet package is missing %s", name)
		}
	}
	nuspec := string(files["SandboxedAgents.nuspec"])
	for _, want := range []string{"<id>SandboxedAgents</id>", "<version>" + releasetest.ReleaseVersion + "</version>", "https://github.com/grauzone-labs/sandboxed-agents", "<developmentDependency>true</developmentDependency>"} {
		if !strings.Contains(nuspec, want) {
			t.Errorf("NuGet metadata does not contain %q", want)
		}
	}
	if strings.Contains(nuspec, "<packageTypes>") {
		t.Error("NuGet package declares a custom type incompatible with ordinary NuGet acquisition")
	}
	var checksums strings.Builder
	for _, name := range []string{linuxArtifact, windowsArtifact} {
		binary, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(binary), name)
		if name == windowsArtifact && !bytes.Equal(files["tools/"+name], binary) {
			t.Error("NuGet executable differs from release executable")
		}
	}
	if string(files["tools/SHA256SUMS"]) != checksums.String() {
		t.Error("NuGet checksums do not cover release executable bytes")
	}
	standaloneChecksums, err := os.ReadFile(filepath.Join(output, "SHA256SUMS"))
	if err != nil || string(standaloneChecksums) != checksums.String() {
		t.Fatalf("standalone checksums = %q, error=%v; want %q", standaloneChecksums, err, checksums.String())
	}
}

// TestWindowsNuGetCommandPackageLifecycle proves a built NuGet command package installs, runs its real Windows executable's version command, and removes it through the extracted PowerShell scripts with a custom directory and no PATH update.
func TestWindowsNuGetCommandPackageLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("NuGet command package executable lifecycle requires Windows")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Fatal("NuGet command package lifecycle requires PowerShell 7 (pwsh)")
	}
	checkout := createReleaseRepository(t, repositoryRoot(t))
	tagReleaseRepository(t, checkout)
	output := t.TempDir()
	buildRelease(t, checkout, output)
	archive, err := zip.OpenReader(filepath.Join(output, "SandboxedAgents."+releasetest.ReleaseVersion+".nupkg"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	extracted := t.TempDir()
	for _, file := range archive.File {
		if !filepath.IsLocal(file.Name) {
			t.Fatalf("package entry is not a local path: %q", file.Name)
		}
		destination := filepath.Join(extracted, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	install := filepath.Join(t.TempDir(), "installed command with spaces")
	installer := filepath.Join(extracted, "tools", "install-command.ps1")
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", installer, "-InstallDirectory", install, "-NoPathUpdate")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package install: %v\n%s", err, result)
	}
	command = exec.Command(filepath.Join(install, "sandboxed-agents.exe"), "version")
	result, err := command.CombinedOutput()
	want := releasetest.ExpectedVersion(t, releasetest.ReleaseVersion)
	if err != nil || string(result) != want {
		t.Fatalf("installed command version: error=%v output=%q; want %q", err, result, want)
	}
	remover := filepath.Join(extracted, "tools", "remove-command.ps1")
	command = exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", remover, "-InstallDirectory", install, "-NoPathUpdate")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package removal: %v\n%s", err, result)
	}
	if _, err := os.Stat(install); !os.IsNotExist(err) {
		t.Fatalf("package removal left the install directory: error=%v", err)
	}
}
