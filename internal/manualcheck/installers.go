package manualcheck

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// InstallerMethods are the four methods of item `installers-no-podman`, in capture order.
var InstallerMethods = []string{"binary", "npm", "nuget-root", "nuget-default"}

// PinnedFile is a file identified by its path and SHA-256.
type PinnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// InstallerConfiguration pins everything a capture of item `installers-no-podman` depends on,
// before any capture: the candidate, the revision of the criterion, the package of scripts and
// their tests, the frozen inputs, the portable Node runtime, and the native recording and export
// tools. The PowerShell helpers in windows/installers read it and refuse any file whose hash
// changed.
type InstallerConfiguration struct {
	SchemaVersion     int                   `json:"schema_version"`
	Candidate         string                `json:"candidate"`
	CriterionSHA256   string                `json:"criterion_sha256"`
	CriterionSource   string                `json:"criterion_source"`
	PackageDirectory  string                `json:"package_directory"`
	PackageFiles      []PinnedFile          `json:"package_files"`
	InputsManifest    PinnedFile            `json:"inputs_manifest"`
	Inputs            map[string]PinnedFile `json:"inputs"`
	NodeZip           PinnedFile            `json:"node_zip"`
	NodeDirectoryName string                `json:"node_directory_name"`
	NativeTools       map[string]PinnedFile `json:"native_tools"`
	RunsRoot          string                `json:"runs_root"`
	MinFreeBytes      uint64                `json:"min_free_bytes"`
	CreatedUTC        string                `json:"created_utc"`
}

// InstallerConfigureOptions are the inputs of InstallerConfigure.
type InstallerConfigureOptions struct {
	Candidate        string
	CriterionFile    string
	CriterionSHA256  string
	PackageDirectory string
	InputsManifest   string
	NodeZip          string
	Procmon          string
	Wpr              string
	Tracerpt         string
	RunsRoot         string
	MinFreeBytes     uint64
	Output           string
}

// inputsManifest is the frozen-inputs manifest a maintainer prepares on the capture host.
type inputsManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	Commit        string                `json:"commit"`
	Files         map[string]PinnedFile `json:"files"`
	Node          struct {
		Directory string `json:"directory"`
		ZipSHA256 string `json:"zip_sha256"`
	} `json:"node"`
}

// requiredInputs are the frozen inputs every capture needs.
var requiredInputs = []string{"binary", "second_binary", "npm_package", "nuget_package", "sums"}

// packageExtensions are the file kinds of the package that are pinned before a capture.
var packageExtensions = []string{".ps1", ".cs", ".py", ".wprp"}

// requiredPackageFiles are the package files every configuration must pin: the shared functions,
// the item shell, the observer, the export, the trace reviewer, the kernel-trace reader, the raw-log
// reader of pipe information queries, and the recording profile.
var requiredPackageFiles = []string{"Package.ps1", "Installer-ItemShell.ps1", "Start-InstallerCapture.ps1", "Export-InstallerCapture.ps1", "Review-InstallerTrace.ps1", "Read-KernelTraceEvidence.ps1", "KernelTrace.cs", "Read-PmlPipeQueries.ps1", "PmlPipeQueries.cs", "InstallerJournal.wprp"}

// packageFiles lists the package's pinned files relative to its directory, sorted.
func packageFiles(directory string) ([]PinnedFile, error) {
	var files []PinnedFile
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !slices.Contains(packageExtensions, strings.ToLower(filepath.Ext(path))) {
			return nil
		}
		sha, err := FileSHA256(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		files = append(files, PinnedFile{Path: filepath.ToSlash(relative), SHA256: sha})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) == 0 {
		return nil, fmt.Errorf("no package files under %s", directory)
	}
	return files, nil
}

// pinFile hashes a file and returns its pin.
func pinFile(path string) (PinnedFile, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return PinnedFile{}, err
	}
	sha, err := FileSHA256(absolute)
	if err != nil {
		return PinnedFile{}, err
	}
	return PinnedFile{Path: absolute, SHA256: sha}, nil
}

// InstallerConfigure writes the capture configuration: it hashes the package, the inputs named by
// the manifest (and checks them against the manifest), the Node ZIP, and the native tools, and
// records the candidate and the criterion revision. It runs no installer and no capture.
func InstallerConfigure(options InstallerConfigureOptions) (*InstallerConfiguration, error) {
	if !commitPattern.MatchString(options.Candidate) {
		return nil, errors.New("candidate must be the 40-character preview commit")
	}
	if options.Output == "" || options.InputsManifest == "" || options.NodeZip == "" || options.Procmon == "" || options.RunsRoot == "" || options.PackageDirectory == "" {
		return nil, errors.New("output, package directory, inputs manifest, node zip, procmon, and runs root are required")
	}
	configuration := &InstallerConfiguration{SchemaVersion: 1, Candidate: options.Candidate, MinFreeBytes: options.MinFreeBytes, CreatedUTC: utcNow()}
	switch {
	case options.CriterionFile != "":
		sha, err := FileSHA256(options.CriterionFile)
		if err != nil {
			return nil, err
		}
		configuration.CriterionSHA256, configuration.CriterionSource = sha, "sha256 of "+filepath.Base(options.CriterionFile)
	case sha256Pattern.MatchString(options.CriterionSHA256):
		configuration.CriterionSHA256, configuration.CriterionSource = options.CriterionSHA256, "given"
	default:
		return nil, errors.New("the criterion revision is required: the checklist file or its SHA-256")
	}
	packageDirectory, err := filepath.Abs(options.PackageDirectory)
	if err != nil {
		return nil, err
	}
	configuration.PackageDirectory = packageDirectory
	if configuration.PackageFiles, err = packageFiles(packageDirectory); err != nil {
		return nil, err
	}
	for _, required := range requiredPackageFiles {
		if !slices.ContainsFunc(configuration.PackageFiles, func(file PinnedFile) bool { return file.Path == required }) {
			return nil, fmt.Errorf("package file %s is missing from %s", required, packageDirectory)
		}
	}
	if configuration.InputsManifest, err = pinFile(options.InputsManifest); err != nil {
		return nil, err
	}
	var manifest inputsManifest
	if err := readJSON(configuration.InputsManifest.Path, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 || manifest.Commit != options.Candidate {
		return nil, fmt.Errorf("the inputs manifest names commit %q, not the candidate", manifest.Commit)
	}
	configuration.Inputs = map[string]PinnedFile{}
	for _, name := range requiredInputs {
		entry, ok := manifest.Files[name]
		if !ok {
			return nil, fmt.Errorf("the inputs manifest lacks %s", name)
		}
		pin, err := pinFile(entry.Path)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(pin.SHA256, entry.SHA256) {
			return nil, fmt.Errorf("frozen input %s has hash %s, the manifest says %s", name, pin.SHA256, entry.SHA256)
		}
		configuration.Inputs[name] = pin
	}
	if configuration.NodeZip, err = pinFile(options.NodeZip); err != nil {
		return nil, err
	}
	if manifest.Node.ZipSHA256 != "" && !strings.EqualFold(manifest.Node.ZipSHA256, configuration.NodeZip.SHA256) {
		return nil, fmt.Errorf("the Node ZIP hash %s differs from the manifest's %s", configuration.NodeZip.SHA256, manifest.Node.ZipSHA256)
	}
	configuration.NodeDirectoryName = lastSegment(manifest.Node.Directory)
	if configuration.NodeDirectoryName == "" {
		configuration.NodeDirectoryName = strings.TrimSuffix(filepath.Base(configuration.NodeZip.Path), ".zip")
	}
	configuration.NativeTools = map[string]PinnedFile{}
	for name, path := range map[string]string{"procmon": options.Procmon, "wpr": orDefaultPath(options.Wpr, `C:\Windows\System32\wpr.exe`), "tracerpt": orDefaultPath(options.Tracerpt, `C:\Windows\System32\tracerpt.exe`)} {
		pin, err := pinFile(path)
		if err != nil {
			return nil, fmt.Errorf("native tool %s: %w", name, err)
		}
		configuration.NativeTools[name] = pin
	}
	if configuration.RunsRoot, err = filepath.Abs(options.RunsRoot); err != nil {
		return nil, err
	}
	if err := writePrivateJSON(options.Output, configuration); err != nil {
		return nil, err
	}
	return configuration, nil
}

// lastSegment returns the last path segment of a Windows or POSIX path, on any host.
func lastSegment(path string) string {
	path = strings.TrimRight(path, `\/`)
	if index := strings.LastIndexAny(path, `\/`); index >= 0 {
		path = path[index+1:]
	}
	if path == "." {
		return ""
	}
	return path
}

// orDefaultPath returns path, or fallback when path is empty.
func orDefaultPath(path, fallback string) string {
	if path == "" {
		return fallback
	}
	return path
}

// LoadInstallerConfiguration reads a configuration and verifies every pinned hash again.
func LoadInstallerConfiguration(path string) (*InstallerConfiguration, error) {
	var configuration InstallerConfiguration
	if err := readJSON(path, &configuration); err != nil {
		return nil, err
	}
	if configuration.SchemaVersion != 1 || !commitPattern.MatchString(configuration.Candidate) || !sha256Pattern.MatchString(configuration.CriterionSHA256) {
		return nil, errors.New("the configuration does not name a candidate and a criterion revision")
	}
	files, err := packageFiles(configuration.PackageDirectory)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(files, configuration.PackageFiles) {
		return nil, errors.New("the package files differ from the configuration; configure again before a capture")
	}
	check := func(name string, pin PinnedFile) error {
		sha, err := FileSHA256(pin.Path)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !strings.EqualFold(sha, pin.SHA256) {
			return fmt.Errorf("%s changed since the configuration: %s", name, pin.Path)
		}
		return nil
	}
	if err := check("inputs manifest", configuration.InputsManifest); err != nil {
		return nil, err
	}
	for _, name := range requiredInputs {
		pin, ok := configuration.Inputs[name]
		if !ok {
			return nil, fmt.Errorf("the configuration lacks input %s", name)
		}
		if err := check("input "+name, pin); err != nil {
			return nil, err
		}
	}
	if err := check("node zip", configuration.NodeZip); err != nil {
		return nil, err
	}
	for _, name := range []string{"procmon", "wpr", "tracerpt"} {
		pin, ok := configuration.NativeTools[name]
		if !ok {
			return nil, fmt.Errorf("the configuration lacks native tool %s", name)
		}
		if err := check("native tool "+name, pin); err != nil {
			return nil, err
		}
	}
	return &configuration, nil
}

// installerPrepared is what the item shell and the elevated observer read about one capture run.
type installerPrepared struct {
	SchemaVersion int                   `json:"schema_version"`
	RunID         string                `json:"run_id"`
	Method        string                `json:"method"`
	InstallRoot   string                `json:"install_root"`
	FakeCalls     string                `json:"fake_calls"`
	Inputs        map[string]PinnedFile `json:"inputs"`
	NodeDirectory string                `json:"node_directory"`
	Configuration PinnedFile            `json:"configuration"`
	Candidate     string                `json:"candidate"`
	Criterion     string                `json:"criterion_sha256"`
	UTC           string                `json:"utc"`
}

// InstallersPrepare prepares one capture run of one method on Windows: it verifies the
// configuration, creates the one-shot run directory and a fresh install root with the fake Podman
// first on PATH, unpacks the pinned Node runtime and, for the NuGet methods, the package, writes
// prepared.private.json, and opens the normal item shell. No installer and no capture starts
// here; the elevated observer and the capture are the person's steps, printed as the checkpoint.
func (r *Run) InstallersPrepare(configurationPath, method, captureID string) error {
	if r.Platform != PlatformWindows {
		return errors.New("installers-no-podman runs on windows-11 only")
	}
	if !slices.Contains(InstallerMethods, method) {
		return fmt.Errorf("method must be one of %v", InstallerMethods)
	}
	configuration, err := LoadInstallerConfiguration(configurationPath)
	if err != nil {
		return err
	}
	if configuration.Candidate != r.Commit {
		return fmt.Errorf("the configuration pins candidate %s, the run is %s", configuration.Candidate, r.Commit)
	}
	configurationPin, err := pinFile(configurationPath)
	if err != nil {
		return err
	}
	dir, err := r.ItemDir("installers-no-podman")
	if err != nil {
		return err
	}
	runID := method + "-" + r.ID
	if captureID != "" {
		if !runIDPattern.MatchString(captureID) || !strings.HasPrefix(captureID, method+"-") {
			return fmt.Errorf("capture ID must match %s and start with %s-", runIDPattern, method)
		}
		runID = captureID
	}
	captureRun := filepath.Join(configuration.RunsRoot, runID)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	installRoot := filepath.Join(home, "chk-install-"+runID)
	for _, path := range []string{captureRun, installRoot} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists; a capture run is one-shot, use a new run ID and preserve the old directory", path)
		}
	}
	free, err := freeBytes(filepath.VolumeName(home) + `\`)
	if err != nil {
		return err
	}
	if free < configuration.MinFreeBytes {
		return fmt.Errorf("free space %d bytes is below the configured %d bytes", free, configuration.MinFreeBytes)
	}
	if err := os.MkdirAll(captureRun, 0700); err != nil {
		return err
	}
	for _, name := range []string{"bin", "prefix", "cache", "logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(installRoot, name), 0700); err != nil {
			return err
		}
	}
	fake := filepath.Join(captureRun, "fake-podman")
	if err := os.MkdirAll(fake, 0700); err != nil {
		return err
	}
	calls := filepath.Join(fake, "calls")
	if err := writePrivate(filepath.Join(fake, "podman.cmd"), "@echo off\r\necho %* >> \""+calls+"\"\r\nexit /b 1\r\n"); err != nil {
		return err
	}
	if err := unzip(configuration.NodeZip.Path, filepath.Join(installRoot, "runtime")); err != nil {
		return err
	}
	nodeDirectory := filepath.Join(installRoot, "runtime", configuration.NodeDirectoryName)
	if _, err := os.Stat(filepath.Join(nodeDirectory, "node.exe")); err != nil {
		return fmt.Errorf("the Node ZIP did not unpack %s: %w", filepath.Join(nodeDirectory, "node.exe"), err)
	}
	if strings.HasPrefix(method, "nuget") {
		if err := unzip(configuration.Inputs["nuget_package"].Path, filepath.Join(installRoot, "package")); err != nil {
			return err
		}
	}
	prepared := installerPrepared{SchemaVersion: 3, RunID: runID, Method: method, InstallRoot: installRoot, FakeCalls: calls, Inputs: configuration.Inputs,
		NodeDirectory: nodeDirectory, Configuration: configurationPin, Candidate: configuration.Candidate, Criterion: configuration.CriterionSHA256, UTC: utcNow()}
	if err := writePrivateJSON(filepath.Join(captureRun, "prepared.private.json"), prepared); err != nil {
		return err
	}
	if err := writePrivateJSON(filepath.Join(dir, runID+".prepared.json"), map[string]string{"capture_run": captureRun, "install_root": installRoot, "method": method, "utc": prepared.UTC}); err != nil {
		return err
	}
	observer := filepath.Join(configuration.PackageDirectory, "Start-InstallerCapture.ps1")
	if err := r.Checkpoint(dir,
		fmt.Sprintf("Leave the item shell that opens now open. In a separate, interactive PowerShell 7 elevated with a separate administrator account, run: & '%s' -RunDirectory '%s'", observer, captureRun),
		"In the Process Monitor window: reset the filters; Enable Advanced Output off; Drop Filtered Events off; File System, Registry, and Process/Thread activity on; the Date & Time, Thread ID, and Duration columns on; the event counter increasing. Press Enter in the observer.",
		"After CAPTURE_READY, in the item shell run, one at a time: Begin-InstallerMethod; Invoke-InstallerStep install; Invoke-InstallerStep reinstall; Invoke-InstallerStep remove.",
		fmt.Sprintf("After CAPTURE_STOPPED run Save-InstallerMethod in the item shell, wait for REVIEW_COMPLETE, then: manual-check installers summarize --run %s --run-directory '%s'", r.ID, captureRun),
	); err != nil {
		return err
	}
	shell, err := itemShellCommand(configuration.PackageDirectory)
	if err != nil {
		return err
	}
	shell.Env = withEnvironment(os.Environ(), map[string]string{
		"Path":                        fake + ";" + nodeDirectory + ";" + os.Getenv("Path"),
		"TEMP":                        filepath.Join(installRoot, "tmp"),
		"TMP":                         filepath.Join(installRoot, "tmp"),
		"PSModuleAnalysisCachePath":   filepath.Join(installRoot, "cache", "ModuleAnalysisCache"),
		"POWERSHELL_TELEMETRY_OPTOUT": "1",
		"POWERSHELL_UPDATECHECK":      "Off",
		"npm_config_update_notifier":  "false",
		"CHK_V2_RUN":                  captureRun,
		"CHK_INSTALLER_CONFIGURATION": configurationPin.Path,
	})
	shell.Stdin, shell.Stdout, shell.Stderr = os.Stdin, os.Stdout, os.Stderr
	r.Printf("RUN_DIRECTORY=%s\nopening the normal item shell; it stays open for the whole method\n", captureRun)
	return shell.Run()
}

// itemShellCommand builds the item shell launch with the resolved pwsh path as argument zero. The
// native command line then begins with the quoted full path of pwsh.exe, which Get-OwnedProcess
// in Package.ps1 translates from the managed pwsh.dll token the shell records; a bare "pwsh"
// argument zero would never match that exact translation.
func itemShellCommand(packageDirectory string) (*exec.Cmd, error) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		return nil, fmt.Errorf("pwsh is required for the item shell: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	command := exec.Command(path, "-NoLogo", "-NoProfile", "-NoExit", "-File", filepath.Join(packageDirectory, "Installer-ItemShell.ps1"))
	command.Args[0] = path
	return command, nil
}

// withEnvironment replaces variables in an environment, matching names without regard to case
// as Windows does.
func withEnvironment(base []string, values map[string]string) []string {
	var result []string
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		replaced := false
		for name := range values {
			if strings.EqualFold(name, key) {
				replaced = true
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

// unzip extracts a ZIP archive below destination and refuses entries that escape it.
func unzip(source, destination string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	root := filepath.Clean(destination) + string(filepath.Separator)
	for _, entry := range reader.File {
		target := filepath.Join(destination, filepath.FromSlash(entry.Name))
		if !strings.HasPrefix(target+string(filepath.Separator), root) && target != filepath.Clean(destination) {
			return fmt.Errorf("zip entry escapes the destination: %s", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0700)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// InstallerResult is the summary the elevated observer's export writes as result.json.
type InstallerResult struct {
	SchemaVersion int    `json:"schema_version"`
	UTC           string `json:"utc"`
	Method        string `json:"method"`
	TraceVerdict  string `json:"trace_verdict"`
	// EnvironmentVerdict is the environment reviewer's verdict over the HKCU\Environment snapshots;
	// anything but NO_FINDINGS, including an absent value, keeps the run from being clean.
	EnvironmentVerdict string `json:"environment_verdict"`
	ExportedRows       int    `json:"exported_rows"`
	BookkeepingRows    int    `json:"bookkeeping_rows"`
	UnknownRows        int    `json:"unknown_rows"`
	OutsideChanges     int    `json:"outside_changes"`
	KernelComplete     bool   `json:"kernel_complete"`
	KernelFailure      string `json:"kernel_failure"`
	// NamedPipeRows counts the rows accepted as communication over named pipes, and PipeQueryRows
	// the pipe information queries among the <Unknown> rows that the raw log proved; a failure of
	// that proof is reported, and no row is accepted on its account.
	NamedPipeRows    int    `json:"named_pipe_rows"`
	PipeQueryRows    int    `json:"pipe_query_rows"`
	PipeQueryFailure string `json:"pipe_query_failure"`
	FakePodmanBytes  int64  `json:"fake_podman_bytes"`
	CapturedCommit   string `json:"captured_commit"`
	CriterionSHA256  string `json:"criterion_sha256"`
}

// installerOutputRoot returns the directory that holds the protected output directory of every
// capture run, below the trusted base of the elevated observer; tests replace it.
var installerOutputRoot = func() (string, error) {
	data, err := programDataDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "sandboxed-agents-manual", "output"), nil
}

// verifyInstallerOutput refuses an output directory that the elevated observer did not create; tests
// replace it together with installerOutputRoot.
var verifyInstallerOutput = verifyProtectedOutput

// SummarizeInstallerRun reads the result in a capture run's protected output directory and returns the observation text and whether
// the local triage found nothing. The text is never an acceptance pass: it reports counts the
// reviewer produced, and the verdict of the method stays with the review of the evidence.
func SummarizeInstallerRun(outputDirectory, candidate string) (string, bool, error) {
	for _, failed := range []string{"capture.failed.json", "export.failed.json"} {
		if data, err := os.ReadFile(filepath.Join(outputDirectory, failed)); err == nil {
			return fmt.Sprintf("%s: %s", failed, strings.TrimSpace(string(data))), false, nil
		}
	}
	var result InstallerResult
	if err := readJSON(filepath.Join(outputDirectory, "result.json"), &result); err != nil {
		return "", false, fmt.Errorf("no result.json yet in %s: %w", outputDirectory, err)
	}
	clean := result.TraceVerdict == "NO_FINDINGS" && result.EnvironmentVerdict == "NO_FINDINGS" && result.UnknownRows == 0 && result.OutsideChanges == 0 && result.KernelComplete && result.FakePodmanBytes == 0 && result.CapturedCommit == candidate
	text := fmt.Sprintf("method %s: trace %s; environment %s; %d rows exported; %d bookkeeping rows; %d named-pipe rows, %d of them pipe information queries proven by the raw log%s; %d unknown rows; %d changes outside the allowed locations; kernel trace complete %t%s; fake Podman bytes %d; captured commit %s; criterion %s. Local triage only; the method passes only on the review of the evidence.",
		result.Method, result.TraceVerdict, orDefaultPath(result.EnvironmentVerdict, "MISSING"), result.ExportedRows, result.BookkeepingRows, result.NamedPipeRows, result.PipeQueryRows, failureNote(result.PipeQueryFailure), result.UnknownRows, result.OutsideChanges, result.KernelComplete, failureNote(result.KernelFailure), result.FakePodmanBytes, result.CapturedCommit, result.CriterionSHA256)
	return text, clean, nil
}

// failureNote formats a failure text of the summary, from the kernel-trace reader or the pipe-query
// proof, in parentheses; an empty failure adds nothing.
func failureNote(failure string) string {
	if failure == "" {
		return ""
	}
	return " (" + failure + ")"
}

// InstallersSummarize records the summary of one capture run under the item directory. The observer
// and the export write only into the run's protected output directory, which the item account may
// read, so the summary reads it there, by the run directory's name, and not from the run directory.
// The name must be a capture run ID, and the output directory and its two parents must be the
// observer's: no link, and owned by Administrators or SYSTEM.
func (r *Run) InstallersSummarize(runDirectory string) error {
	if runDirectory == "" {
		return errors.New("the capture run directory is required")
	}
	dir, err := r.ItemDir("installers-no-podman")
	if err != nil {
		return err
	}
	name := filepath.Base(runDirectory)
	if !runIDPattern.MatchString(name) {
		return fmt.Errorf("%s is not the name of a capture run directory", runDirectory)
	}
	root, err := installerOutputRoot()
	if err != nil {
		return err
	}
	output := filepath.Join(root, name)
	if err := verifyInstallerOutput(output); err != nil {
		return err
	}
	text, clean, err := SummarizeInstallerRun(output, r.Commit)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, name+".summary.txt"), text+"\n"); err != nil {
		return err
	}
	if err := r.Verdict(dir, "installers-no-podman ("+name+")", !clean, text); err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("installers-no-podman: %s has findings or an incomplete capture", name)
	}
	return nil
}
