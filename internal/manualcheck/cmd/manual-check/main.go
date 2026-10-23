// Command manual-check runs the mechanical parts of docs/manual-checklist.md on Linux and
// Windows and keeps the record of a run. It stops at every step only a person can perform and
// never records a pass by itself.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/manualcheck"
)

// usage lists the commands, one per item or item part of the checklist.
const usage = `usage: manual-check COMMAND [OPTIONS]

Run setup:
  preflight       --run ID --commit SHA --version V --asset-sha256 H [--executable PATH] [--executable-sha256 H] [--group G] [--platform P] [--podman-connection M] [--min-free-bytes N] [--parent DIR]

Items (each needs a verified run; --parent selects the directory of run directories):
  persistence setup         --run ID --github-repo URL --azdo-organization URL --azdo-repo URL [--sandbox chk-persist]
  persistence observe       --run ID --round N
  persistence make-outdated --run ID
  persistence update        --run ID
  persistence verify-update --run ID
  refusals no-terminal      --run ID [--sandbox chk-persist]
  refusals toolchain        --run ID [--keep]
  interrupted-update        --run ID [--sandbox chk-int] [--keep]
  vscode prepare|verify|cleanup --run ID [--sandbox chk-vs] [--file NAME]
  npm-clean                 --run ID --tarball FILE [--install-root DIR]
  nuget-clean prepare|after-install|after-reinstall|after-remove --run ID [--package FILE] [--install-root DIR]
  attestation               --run ID --tag TAG [--repository OWNER/REPO]
  images inventory|compare  --run ID [--name N] [--before N --after N]
  final-cleanup             --run ID [--sandbox chk-persist]
  installers configure|prepare|summarize   (Windows; prepare takes --configuration FILE --method M [--capture-id ID]; see docs/manual-checklist.md)

The record:
  record ids
  record init     FILE --commit SHA
  record add      FILE --name ID --result pass|fail --date YYYY-MM-DD --platform linux|windows-11 [--replace] [--human-observed]
  record status   FILE
  record validate FILE
`

// main exits with the command's status.
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run dispatches one command and reports its outcome; usage errors exit 2, failed observations 1.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "record":
		err = recordCommand(args[1:], stdout)
	case "preflight":
		err = preflightCommand(args[1:])
	default:
		err = itemCommand(args, stdout)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "manual-check: %v\n", err)
	if strings.HasPrefix(err.Error(), "usage") {
		return 2
	}
	return 1
}

// flags creates a flag set that reports usage errors instead of exiting.
func flags(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

// recordCommand handles the record subcommands.
func recordCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: record ids|init|add|status|validate")
	}
	switch args[0] {
	case "ids":
		for _, id := range manualcheck.Identifiers {
			fmt.Fprintln(stdout, id)
		}
		return nil
	case "init":
		set := flags("record init")
		commit := set.String("commit", "", "preview commit")
		if len(args) < 2 || set.Parse(args[2:]) != nil || set.NArg() != 0 {
			return fmt.Errorf("usage: record init FILE --commit SHA")
		}
		file := args[1]
		if _, err := os.Stat(file); err == nil {
			return fmt.Errorf("record exists: %s; a new run gets a new record", file)
		}
		record, err := manualcheck.NewRecord(*commit)
		if err != nil {
			return err
		}
		if err := record.Save(file); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created %s for commit %s; it validates once the first check is recorded\n", file, *commit)
		return nil
	case "add":
		set := flags("record add")
		name := set.String("name", "", "identifier")
		result := set.String("result", "", "pass or fail")
		date := set.String("date", "", "YYYY-MM-DD")
		platform := set.String("platform", "", "linux or windows-11")
		replace := set.Bool("replace", false, "replace an existing check")
		human := set.Bool("human-observed", false, "a person observed the pass")
		if len(args) < 2 || set.Parse(args[2:]) != nil || set.NArg() != 0 {
			return fmt.Errorf("usage: record add FILE --name ID --result pass|fail --date YYYY-MM-DD --platform P [--replace] [--human-observed]")
		}
		file := args[1]
		record, err := manualcheck.LoadRecord(file)
		if err != nil {
			return err
		}
		if err := record.AddCheck(manualcheck.Check{Name: *name, Result: *result, Date: *date, Platform: *platform}, *replace, *human); err != nil {
			return err
		}
		if err := record.Save(file); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "recorded %s %s (%s, %s); record result %s\n", *name, *result, *date, *platform, record.Result)
		return nil
	case "status", "validate":
		if len(args) != 2 {
			return fmt.Errorf("usage: record %s FILE", args[0])
		}
		record, err := manualcheck.LoadRecord(args[1])
		if err != nil {
			return err
		}
		if args[0] == "status" {
			if len(record.Checks) > 0 {
				if err := record.Validate(); err != nil {
					return fmt.Errorf("record does not validate: %w", err)
				}
			}
			fmt.Fprint(stdout, record.Status())
			return nil
		}
		if err := record.Validate(); err != nil {
			return fmt.Errorf("invalid: %s: %w", args[1], err)
		}
		fmt.Fprintf(stdout, "valid: %s\n", args[1])
		return nil
	}
	return fmt.Errorf("usage: record ids|init|add|status|validate")
}

// preflightCommand verifies the preview under test and creates the run.
func preflightCommand(args []string) error {
	set := flags("preflight")
	var options manualcheck.PreflightOptions
	set.StringVar(&options.Parent, "parent", "", "directory of run directories")
	set.StringVar(&options.ID, "run", "", "run ID")
	set.StringVar(&options.Commit, "commit", "", "preview commit")
	set.StringVar(&options.Version, "version", "", "preview version")
	set.StringVar(&options.AssetSHA256, "asset-sha256", "", "asset hash the preview prints")
	set.StringVar(&options.Executable, "executable", "", "verified executable")
	set.StringVar(&options.ExecutableSHA256, "executable-sha256", "", "SHA-256 of the executable from SHA256SUMS")
	set.StringVar(&options.Group, "group", "manual-check", "dedicated controller group")
	set.StringVar(&options.Platform, "platform", "", "linux or windows-11")
	set.StringVar(&options.PodmanConnection, "podman-connection", "", "Podman machine on Windows")
	set.Uint64Var(&options.MinFreeBytes, "min-free-bytes", 0, "space gate")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return fmt.Errorf("usage: preflight --run ID --commit SHA --version V --asset-sha256 H [options]")
	}
	// Preflight prints its progress itself; nothing of it goes through stdout afterwards.
	_, err := manualcheck.Preflight(options)
	return err
}

// itemCommand loads the verified run and dispatches one item command.
func itemCommand(args []string, stdout io.Writer) error {
	name := args[0]
	rest := args[1:]
	sub := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub, rest = rest[0], rest[1:]
	}
	if name == "installers" && sub == "configure" {
		return installersConfigure(rest, stdout)
	}
	set := flags(name)
	parent := set.String("parent", "", "directory of run directories")
	id := set.String("run", "", "run ID")
	sandbox := set.String("sandbox", "", "sandbox name")
	keep := set.Bool("keep", false, "keep the item's sandboxes")
	round := set.Int("round", -1, "round number")
	githubRepo := set.String("github-repo", "", "private test repository URL")
	azdoOrganization := set.String("azdo-organization", "", "Azure DevOps organization URL")
	azdoRepo := set.String("azdo-repo", "", "Azure DevOps repository URL")
	file := set.String("file", "", "file name")
	tarball := set.String("tarball", "", "npm tarball")
	pkg := set.String("package", "", "NuGet package")
	installRoot := set.String("install-root", "", "install root")
	tag := set.String("tag", "", "preview tag")
	repository := set.String("repository", "", "GitHub owner/repository")
	inventory := set.String("name", "", "inventory name")
	before := set.String("before", "", "earlier inventory")
	after := set.String("after", "", "later inventory")
	configuration := set.String("configuration", "", "installer capture configuration")
	method := set.String("method", "", "installer method")
	runDirectory := set.String("run-directory", "", "installer capture run directory")
	captureID := set.String("capture-id", "", "name of the one-shot capture run (default METHOD-RUN)")
	if err := set.Parse(rest); err != nil || set.NArg() != 0 {
		return fmt.Errorf("usage: %s", strings.TrimSpace(usage))
	}
	if *id == "" {
		return fmt.Errorf("usage: %s needs --run ID", name)
	}
	root, err := manualcheck.RunRoot(*parent, *id)
	if err != nil {
		return err
	}
	run, err := manualcheck.LoadRun(root)
	if err != nil {
		return err
	}
	run.Stdout = stdout
	switch name + " " + sub {
	case "persistence setup":
		return run.PersistenceSetup(manualcheck.PersistenceSetupOptions{Sandbox: *sandbox, GitHubRepo: *githubRepo, AzdoOrganization: *azdoOrganization, AzdoRepo: *azdoRepo})
	case "persistence observe":
		return run.PersistenceObserve(*round)
	case "persistence make-outdated":
		return run.PersistenceMakeOutdated()
	case "persistence update":
		return run.PersistenceUpdate()
	case "persistence verify-update":
		return run.PersistenceVerifyUpdate()
	case "refusals no-terminal":
		return run.LoginNeedsTerminal(orDefault(*sandbox, "chk-persist"))
	case "refusals toolchain":
		return run.AzureToolchainRequired(*keep)
	case "interrupted-update ":
		return run.UpdateInterrupted(orDefault(*sandbox, "chk-int"), *keep)
	case "vscode prepare":
		return run.VSCodePrepare(*sandbox)
	case "vscode verify":
		return run.VSCodeVerify(*sandbox, *file)
	case "vscode cleanup":
		return run.VSCodeCleanup(*sandbox)
	case "npm-clean ":
		return run.NpmInstallClean(manualcheck.NpmCleanOptions{Tarball: *tarball, InstallRoot: *installRoot})
	case "nuget-clean prepare", "nuget-clean after-install", "nuget-clean after-reinstall", "nuget-clean after-remove":
		return run.NuGetInstallClean(sub, manualcheck.NuGetCleanOptions{Package: *pkg, InstallRoot: *installRoot})
	case "attestation ":
		return run.PreviewAttestation(*tag, *repository)
	case "images inventory":
		_, err := run.ImagesInventory(*inventory)
		return err
	case "images compare":
		return run.ImagesCompare(*before, *after)
	case "final-cleanup ":
		return run.FinalCleanup(*sandbox)
	case "installers prepare":
		return run.InstallersPrepare(*configuration, *method, *captureID)
	case "installers summarize":
		return run.InstallersSummarize(*runDirectory)
	}
	return fmt.Errorf("usage: %s", strings.TrimSpace(usage))
}

// installersConfigure writes the capture configuration of the installer item from its flags.
func installersConfigure(args []string, stdout io.Writer) error {
	set := flags("installers configure")
	var options manualcheck.InstallerConfigureOptions
	set.StringVar(&options.Candidate, "candidate", "", "preview commit")
	set.StringVar(&options.CriterionFile, "criterion-file", "", "docs/manual-checklist.md revision applied")
	set.StringVar(&options.CriterionSHA256, "criterion-sha256", "", "SHA-256 of the checklist revision, instead of the file")
	set.StringVar(&options.PackageDirectory, "package-directory", "", "directory of the installer evidence package")
	set.StringVar(&options.InputsManifest, "inputs-manifest", "", "frozen inputs manifest")
	set.StringVar(&options.NodeZip, "node-zip", "", "portable Node.js ZIP")
	set.StringVar(&options.Procmon, "procmon", "", "Procmon64.exe")
	set.StringVar(&options.Wpr, "wpr", "", "wpr.exe (default C:\\Windows\\System32\\wpr.exe)")
	set.StringVar(&options.Tracerpt, "tracerpt", "", "tracerpt.exe (default C:\\Windows\\System32\\tracerpt.exe)")
	set.StringVar(&options.RunsRoot, "runs-root", "", "directory of capture runs, outside the user profile")
	set.Uint64Var(&options.MinFreeBytes, "min-free-bytes", 20<<30, "free space the capture and export need")
	set.StringVar(&options.Output, "out", "", "configuration file to write")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return fmt.Errorf("usage: installers configure --candidate SHA --criterion-file FILE --package-directory DIR --inputs-manifest FILE --node-zip FILE --procmon FILE --runs-root DIR --out FILE")
	}
	configuration, err := manualcheck.InstallerConfigure(options)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "configured %s: candidate %s, criterion %s, %d package files, %d inputs, %d native tools pinned\n", options.Output, configuration.Candidate, configuration.CriterionSHA256, len(configuration.PackageFiles), len(configuration.Inputs), len(configuration.NativeTools))
	return nil
}

// orDefault returns value, or fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
