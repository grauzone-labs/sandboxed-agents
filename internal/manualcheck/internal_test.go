package manualcheck

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestComparePathEntriesIsByteForByte proves the stored Path comparison reports added and removed
// entries exactly, unexpanded and without touching the others, including a duplicate entry.
func TestComparePathEntriesIsByteForByte(t *testing.T) {
	before := `%USERPROFILE%\bin;C:\Tools;C:\Tools`
	after := `%USERPROFILE%\bin;C:\Tools;C:\Tools;C:\Users\x\AppData\Local\Programs\sandboxed-agents`
	added, removed := comparePathEntries(before, after)
	if !slices.Equal(added, []string{`C:\Users\x\AppData\Local\Programs\sandboxed-agents`}) || len(removed) != 0 {
		t.Fatalf("added %v removed %v", added, removed)
	}
	added, removed = comparePathEntries(after, `%USERPROFILE%\bin;C:\Tools;C:\Users\x\AppData\Local\Programs\sandboxed-agents`)
	if len(added) != 0 || !slices.Equal(removed, []string{`C:\Tools`}) {
		t.Fatalf("a dropped duplicate was not reported: added %v removed %v", added, removed)
	}
	added, removed = comparePathEntries(before, strings.ToLower(before))
	if len(added) != 3 || len(removed) != 3 {
		t.Fatalf("a case change must show as removed and added entries: %v %v", added, removed)
	}
}

// TestTamperTarballChangesOneMember proves the tampered copy keeps every member and differs only
// in the named executable, through a synthetic tarball.
func TestTamperTarballChangesOneMember(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "sandboxed-agents-1.0.0.tgz")
	writeTarball(t, source, map[string]string{"package/package.json": "{}", "package/sandboxed-agents-linux-amd64": strings.Repeat("x", 100)})
	target := filepath.Join(dir, "tampered.tgz")
	if err := tamperTarball(source, target, "package/sandboxed-agents-linux-amd64"); err != nil {
		t.Fatal(err)
	}
	names, err := members(target)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"package/package.json", "package/sandboxed-agents-linux-amd64"}) {
		t.Fatalf("members %v", names)
	}
	if err := tamperTarball(source, target+"2", "package/missing"); err == nil {
		t.Fatal("a missing member was tampered")
	}
}

// TestWithEnvironmentReplacesCaseInsensitively proves environment replacement treats Path and PATH
// as one variable, as Windows does, and appends the new values in a stable order.
func TestWithEnvironmentReplacesCaseInsensitively(t *testing.T) {
	result := withEnvironment([]string{"PATH=/old", "HOME=/h"}, map[string]string{"Path": "/fake;/old", "TEMP": "/t"})
	if !slices.Equal(result, []string{"HOME=/h", "Path=/fake;/old", "TEMP=/t"}) {
		t.Fatalf("result %v", result)
	}
}

// TestUnzipRefusesEscapingEntries proves the ZIP extraction keeps entries below the destination
// and rejects a path that climbs out of it.
func TestUnzipRefusesEscapingEntries(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.zip")
	writeZip(t, good, map[string]string{"node-v1/node.exe": "exe", "node-v1/npm.cmd": "cmd"})
	if err := unzip(good, filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "node-v1", "node.exe")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.zip")
	writeZip(t, bad, map[string]string{"../escape.txt": "x"})
	if err := unzip(bad, filepath.Join(dir, "out2")); err == nil {
		t.Fatal("an escaping entry was extracted")
	}
}

// TestGhVersionAtLeast proves the gh version floor comparison on the documented boundaries.
func TestGhVersionAtLeast(t *testing.T) {
	for output, want := range map[string]bool{"gh version 2.67.0 (x)": true, "gh version 2.102.0 (x)": true, "gh version 2.66.1 (x)": false, "gh version 3.0.0 (x)": true, "nothing": false} {
		if _, got := ghVersionAtLeast(output, 2, 67, 0); got != want {
			t.Errorf("%q: got %t", output, got)
		}
	}
}

// TestSummarizeInstallerRunNeverPasses proves the summary reports the reviewer's counts and the
// local-triage limit, treats a failed capture or export as not clean, and requires the captured
// commit to be the candidate.
func TestSummarizeInstallerRunNeverPasses(t *testing.T) {
	dir := t.TempDir()
	result := `{"schema_version":2,"method":"binary","trace_verdict":"NO_FINDINGS","environment_verdict":"NO_FINDINGS","exported_rows":10,"bookkeeping_rows":1,"unknown_rows":0,"outside_changes":0,"kernel_complete":true,"kernel_failure":"","fake_podman_bytes":0,"captured_commit":"` + strings.Repeat("a", 40) + `","criterion_sha256":"` + strings.Repeat("c", 64) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
	text, clean, err := SummarizeInstallerRun(dir, strings.Repeat("a", 40))
	if err != nil || !clean || !strings.Contains(text, "Local triage only") || !strings.Contains(text, "environment NO_FINDINGS;") {
		t.Fatalf("%v %t %s", err, clean, text)
	}
	// The environment reviewer's verdict closes the summary: FAIL, INCOMPLETE, or an absent value is never clean.
	for _, verdict := range []string{`"environment_verdict":"FAIL",`, `"environment_verdict":"INCOMPLETE",`, ``} {
		if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(strings.Replace(result, `"environment_verdict":"NO_FINDINGS",`, verdict, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		if text, clean, err := SummarizeInstallerRun(dir, strings.Repeat("a", 40)); err != nil || clean || !strings.Contains(text, "environment ") {
			t.Fatalf("environment verdict %q counted as clean: %v %t %s", verdict, err, clean, text)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
	if _, clean, _ := SummarizeInstallerRun(dir, strings.Repeat("b", 40)); clean {
		t.Fatal("another candidate's capture counted as clean")
	}
	if err := os.WriteFile(filepath.Join(dir, "capture.failed.json"), []byte(`{"failure":"budget"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if text, clean, _ := SummarizeInstallerRun(dir, strings.Repeat("a", 40)); clean || !strings.Contains(text, "capture.failed.json") {
		t.Fatalf("a failed capture counted as clean: %s", text)
	}
}

// TestInstallerConfigurationPinsEverything proves configure hashes the package, the inputs against
// the manifest, the Node ZIP, and the native tools, that loading verifies them again, and that a
// changed package file or input is refused, through synthetic files.
func TestInstallerConfigurationPinsEverything(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "package")
	if err := os.MkdirAll(filepath.Join(pkg, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Package.ps1", "Installer-ItemShell.ps1", "Start-InstallerCapture.ps1", "Export-InstallerCapture.ps1", "Review-InstallerTrace.ps1", "Read-KernelTraceEvidence.ps1", "KernelTrace.cs", "InstallerJournal.wprp", "tests/test_x.py", "README.txt"} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inputs := filepath.Join(dir, "inputs")
	if err := os.MkdirAll(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	manifest := `{"schema_version":1,"commit":"` + strings.Repeat("a", 40) + `","files":{`
	for _, name := range []string{"binary", "second_binary", "npm_package", "nuget_package", "sums"} {
		path := filepath.Join(inputs, name)
		if err := os.WriteFile(path, []byte("input "+name), 0600); err != nil {
			t.Fatal(err)
		}
		sha, _ := FileSHA256(path)
		files[name] = sha
		manifest += `"` + name + `":{"path":"` + filepath.ToSlash(path) + `","sha256":"` + sha + `"},`
	}
	nodeZip := filepath.Join(inputs, "node.zip")
	writeZip(t, nodeZip, map[string]string{"node-v1/node.exe": "n"})
	zipSHA, _ := FileSHA256(nodeZip)
	manifest = strings.TrimSuffix(manifest, ",") + `},"node":{"directory":"C:\\x\\node-v1","zip_sha256":"` + zipSHA + `"}}`
	manifestPath := filepath.Join(inputs, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"procmon.exe", "wpr.exe", "tracerpt.exe"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(tool), 0600); err != nil {
			t.Fatal(err)
		}
	}
	criterion := filepath.Join(dir, "manual-checklist.md")
	if err := os.WriteFile(criterion, []byte("# criterion"), 0600); err != nil {
		t.Fatal(err)
	}
	options := InstallerConfigureOptions{Candidate: strings.Repeat("a", 40), CriterionFile: criterion, PackageDirectory: pkg, InputsManifest: manifestPath, NodeZip: nodeZip,
		Procmon: filepath.Join(dir, "procmon.exe"), Wpr: filepath.Join(dir, "wpr.exe"), Tracerpt: filepath.Join(dir, "tracerpt.exe"), RunsRoot: filepath.Join(dir, "runs"), Output: filepath.Join(dir, "configuration.json")}
	configuration, err := InstallerConfigure(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.PackageFiles) != 9 || configuration.NodeDirectoryName != "node-v1" || configuration.Inputs["binary"].SHA256 != files["binary"] {
		t.Fatalf("configuration %+v", configuration)
	}
	if _, err := LoadInstallerConfiguration(options.Output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Package.ps1"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstallerConfiguration(options.Output); err == nil {
		t.Fatal("a changed package file loaded")
	}
	if err := os.WriteFile(filepath.Join(pkg, "Package.ps1"), []byte("Package.ps1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "binary"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstallerConfiguration(options.Output); err == nil {
		t.Fatal("a changed input loaded")
	}
	if _, err := InstallerConfigure(options); err == nil {
		t.Fatal("an input that differs from the manifest was configured")
	}
	options.Candidate = strings.Repeat("b", 40)
	if _, err := InstallerConfigure(options); err == nil {
		t.Fatal("a manifest of another commit was configured")
	}
}

// writeZip writes a ZIP archive with the given members.
func writeZip(t *testing.T, path string, members map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range members {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSplitConfigComparesMountsAsASet proves the update comparison ignores the order in which
// Podman lists the three mounts and still detects a changed limit or a missing volume.
func TestSplitConfigComparesMountsAsASet(t *testing.T) {
	before := "aaa 8589934592 400000 100000 2048 1073741824 azure 2222\nx.home /home/agent;x.ssh /etc/ssh;x.workspace /workspace;\n"
	after := "bbb 8589934592 400000 100000 2048 1073741824 azure 2222\nx.ssh /etc/ssh;x.workspace /workspace;x.home /home/agent;\n"
	beforeImage, beforeRest := splitConfig(before)
	afterImage, afterRest := splitConfig(after)
	if beforeImage != "aaa" || afterImage != "bbb" || beforeRest != afterRest {
		t.Fatalf("reordered mounts compared unequal: %q vs %q", beforeRest, afterRest)
	}
	_, changedLimit := splitConfig(strings.Replace(after, "2048", "4096", 1))
	_, missingVolume := splitConfig("bbb 8589934592 400000 100000 2048 1073741824 azure 2222\nx.ssh /etc/ssh;x.workspace /workspace;\n")
	if changedLimit == beforeRest || missingVolume == beforeRest {
		t.Fatal("a changed limit or a missing volume compared equal")
	}
}

// TestItemShellCommandUsesFullPathAsArgumentZero proves the item shell is started with the resolved
// pwsh path as argument zero, so the native command line that Win32_Process reports begins with the
// quoted full path that Get-OwnedProcess translates from the managed pwsh.dll token. A bare "pwsh"
// first token can never match, which is the failure the observer reported on 2026-10-22.
func TestItemShellCommandUsesFullPathAsArgumentZero(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "pwsh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	command, err := itemShellCommand(filepath.Join(dir, "package"))
	if err != nil {
		t.Fatal(err)
	}
	if command.Args[0] != command.Path || command.Path != fake {
		t.Fatalf("argument zero %q must be the resolved path %q", command.Args[0], command.Path)
	}
	if got := command.Args[1:5]; !slices.Equal(got, []string{"-NoLogo", "-NoProfile", "-NoExit", "-File"}) {
		t.Fatalf("arguments %v", got)
	}
}

// TestStateDirectoryForPlatform proves the run directory's parent is %LOCALAPPDATA% on Windows,
// with AppData\Local below the home directory as the fallback, and $XDG_STATE_HOME or
// ~/.local/state elsewhere.
func TestStateDirectoryForPlatform(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	cases := []struct {
		goos, want string
		values     map[string]string
	}{
		{"windows", `C:\Users\x\AppData\Local`, map[string]string{"LOCALAPPDATA": `C:\Users\x\AppData\Local`, "XDG_STATE_HOME": "/ignored"}},
		{"windows", filepath.Join("home", "AppData", "Local"), map[string]string{}},
		{"linux", "/state", map[string]string{"XDG_STATE_HOME": "/state", "LOCALAPPDATA": `C:\ignored`}},
		{"linux", filepath.Join("home", ".local", "state"), map[string]string{}},
	}
	for _, c := range cases {
		if got := stateDirectoryFor(c.goos, env(c.values), "home"); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.goos, c.values, got, c.want)
		}
	}
}

// TestNuGetInstallEntryIsTheDefaultCommandDirectory proves the Path entry the NuGet item expects
// is the default command directory below LOCALAPPDATA and that a raw entry matches it without
// regard to case or a trailing separator, while any other directory does not.
func TestNuGetInstallEntryIsTheDefaultCommandDirectory(t *testing.T) {
	entry := defaultInstallEntry(func(name string) string {
		if name == "LOCALAPPDATA" {
			return `C:\Users\x\AppData\Local`
		}
		return ""
	}, "unused")
	if entry != filepath.Join(`C:\Users\x\AppData\Local`, "Programs", "sandboxed-agents") {
		t.Fatalf("entry %q", entry)
	}
	for raw, want := range map[string]bool{entry: true, strings.ToUpper(entry) + `\`: true, filepath.Join(`C:\Users\x\AppData\Local`, "Programs", "other"): false, `C:\Tools`: false} {
		if pathEntryMatches(raw, entry) != want {
			t.Errorf("%q matches %q: want %t", raw, entry, want)
		}
	}
}

// TestCheckSumsReportsEachExecutable proves the portable checksum check reports OK, FAILED, and
// MISSING per file against SHA256SUMS in the sha256sum form, through files in a temporary directory.
func TestCheckSumsReportsEachExecutable(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"sandboxed-agents-linux-amd64": "linux", "sandboxed-agents-windows-amd64.exe": "windows"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	linux, _ := FileSHA256(filepath.Join(dir, "sandboxed-agents-linux-amd64"))
	sums := linux + "  sandboxed-agents-linux-amd64\n" + strings.Repeat("0", 64) + "  sandboxed-agents-windows-amd64.exe\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := checkSums(dir, []string{"sandboxed-agents-linux-amd64", "sandboxed-agents-windows-amd64.exe", "SandboxedAgents.nupkg"})
	if err != nil || report != "sandboxed-agents-linux-amd64: OK\nsandboxed-agents-windows-amd64.exe: FAILED\nSandboxedAgents.nupkg: MISSING\n" {
		t.Fatalf("%v\n%s", err, report)
	}
	if _, err := checkSums(t.TempDir(), nil); err == nil {
		t.Fatal("a directory without SHA256SUMS was checked")
	}
}

// TestStatusProbesLeakNoAccountIdentifier proves the gh and az probe scripts record only the login
// line, the exit statuses, and the account hash, never the account name, tenant, or subscription,
// through fake gh and az programs that print such identifiers.
func TestStatusProbesLeakNoAccountIdentifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probes are bash scripts run inside a sandbox")
	}
	bin := t.TempDir()
	const sentinel = "person@example.invalid"
	fakes := map[string]string{
		"az": "#!/bin/sh\nif [ \"$2\" = show ] && [ \"$3\" = --query ]; then echo " + sentinel + "; exit 0; fi\necho '{\"user\":{\"name\":\"" + sentinel + "\"},\"tenantId\":\"11111111-2222-3333-4444-555555555555\"}'\n",
		"gh": "#!/bin/sh\necho 'github.com'\necho '  Logged in to github.com account " + sentinel + " (keyring)'\nexit 0\n",
	}
	for name, script := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, probe := range []string{ghStatusProbe, azAccountProbe} {
		command := exec.Command("bash", "-c", probe)
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		output, err := command.CombinedOutput()
		text := string(output)
		if err != nil || strings.Contains(text, sentinel) || strings.Contains(text, "1111") {
			t.Fatalf("probe leaked an identifier or failed: %v\n%s", err, text)
		}
		if probe == ghStatusProbe && (!strings.Contains(text, "Logged in to github.com") || !strings.Contains(text, "gh exit 0")) {
			t.Fatalf("gh probe output:\n%s", text)
		}
		if probe == azAccountProbe && (!strings.Contains(text, "az exit 0") || !strings.Contains(text, "az-account-sha256 "+AccountHash(sentinel))) {
			t.Fatalf("az probe output:\n%s", text)
		}
	}
}

// TestPersistenceRoundScriptLeaksNoAccountIdentifier proves the in-sandbox script of a persistence
// round records the GitHub login line, the exit statuses, and the Azure account hash, and never the
// account name gh or az prints, by running the real round script of rounds 0 and 1 under bash
// against fake gh, az, and git programs that print a sentinel identifier.
func TestPersistenceRoundScriptLeaksNoAccountIdentifier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the round script is a bash script run inside a sandbox")
	}
	bin := t.TempDir()
	const sentinel = "person@example.invalid"
	fakes := map[string]string{
		"gh":  "#!/bin/sh\necho 'github.com'\necho '  Logged in to github.com account " + sentinel + " (keyring)'\nexit 0\n",
		"az":  "#!/bin/sh\ncase \"$*\" in\n*'account show'*) echo " + sentinel + ";;\n*'devops project list'*) printf 'ID Name\\nabc project\\n';;\nesac\nexit 0\n",
		"git": "#!/bin/sh\nexit 0\n",
	}
	for name, script := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := PersistenceConfig{Sandbox: "chk-persist", GitHubRepo: "https://github.com/o/r", AzdoOrganization: "https://dev.azure.com/o", AzdoRepo: "https://dev.azure.com/o/p/_git/r"}
	for _, round := range []int{0, 1} {
		command := exec.Command("bash", "-c", roundScript(config, round))
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		output, _ := command.CombinedOutput()
		text := string(output)
		if strings.Contains(text, sentinel) {
			t.Fatalf("round %d logged an account identifier:\n%s", round, text)
		}
		for _, want := range []string{"Logged in to github.com", "gh exit 0", "az-account-sha256 " + AccountHash(sentinel), "azdo-projects exit 0"} {
			if !strings.Contains(text, want) {
				t.Fatalf("round %d output lacks %q:\n%s", round, want, text)
			}
		}
	}
}

// TestNuGetInstallCleanRefusesAnElevatedOrUnreadableToken proves every stage of the NuGet item
// refuses before it changes anything when the process token is elevated or its elevation cannot be
// read, and that a readable, non-elevated token passes the gate, through the injectable elevation
// probe that the Windows build binds to the process token.
func TestNuGetInstallCleanRefusesAnElevatedOrUnreadableToken(t *testing.T) {
	saved := processElevated
	t.Cleanup(func() { processElevated = saved })
	probes := map[string]func() (bool, error){
		"elevated":   func() (bool, error) { return true, nil },
		"unreadable": func() (bool, error) { return false, errors.New("token unreadable") },
	}
	for name, probe := range probes {
		processElevated = probe
		for _, stage := range []string{"prepare", "after-install"} {
			root := t.TempDir()
			run := &Run{Platform: PlatformWindows, Root: root, Version: "1.0.0-preview.20260101.1", Stdout: io.Discard}
			pkg := filepath.Join(t.TempDir(), "SandboxedAgents.1.0.0-preview.20260101.1.nupkg")
			if err := os.WriteFile(pkg, []byte("package"), 0600); err != nil {
				t.Fatal(err)
			}
			installRoot := filepath.Join(t.TempDir(), "chk-nuget")
			err := run.NuGetInstallClean(stage, NuGetCleanOptions{Package: pkg, InstallRoot: installRoot})
			if err == nil || !strings.Contains(err.Error(), "administrator") {
				t.Fatalf("%s token, stage %s: %v", name, stage, err)
			}
			for _, path := range []string{installRoot, filepath.Join(root, "nuget-install-clean")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s token, stage %s: %s was created before the refusal", name, stage, path)
				}
			}
		}
	}
	processElevated = func() (bool, error) { return false, nil }
	if err := requireNotElevated(); err != nil {
		t.Fatalf("a non-elevated token was refused: %v", err)
	}
}
