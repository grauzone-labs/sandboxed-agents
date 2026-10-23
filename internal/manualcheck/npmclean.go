package manualcheck

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// NpmCleanOptions name the preview tarball and the contained install root.
type NpmCleanOptions struct {
	Tarball     string
	InstallRoot string
}

// NpmInstallClean runs item `npm-install-clean`: a contained install of the preview's tarball,
// the installed command's version, check, and list, a tampered copy that fails the checksum, and
// the uninstall. npm writes its prefix, cache, temporary files, and logs under the install root.
// The clean-machine precondition is the operator's; an existing install root or an installed
// command stops the item.
func (r *Run) NpmInstallClean(options NpmCleanOptions) error {
	dir, err := r.ItemDir("npm-install-clean")
	if err != nil {
		return err
	}
	if options.InstallRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		options.InstallRoot = filepath.Join(home, "chk-install")
	}
	if _, err := os.Stat(options.InstallRoot); err == nil {
		return fmt.Errorf("%s exists; start from a clean machine or remove it first", options.InstallRoot)
	}
	if _, err := exec.LookPath("sandboxed-agents"); err == nil {
		return errors.New("sandboxed-agents is already on PATH; the item needs a clean machine")
	}
	tarball, err := filepath.Abs(options.Tarball)
	if err != nil {
		return err
	}
	if filepath.Base(tarball) != "sandboxed-agents-"+r.Version+".tgz" {
		return fmt.Errorf("the tarball must be sandboxed-agents-%s.tgz of the preview under test", r.Version)
	}
	sha, err := FileSHA256(tarball)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "tarball.txt"), "tarball sha256 "+sha+"\n"); err != nil {
		return err
	}
	for _, name := range []string{"bin", "prefix", "prefix2", "cache", "logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(options.InstallRoot, name), 0700); err != nil {
			return err
		}
	}
	root := options.InstallRoot
	npm := func(verb, prefix string, extra ...string) *exec.Cmd {
		args := append([]string{verb, "--offline", "--no-audit", "--no-fund", "-g", "--prefix", filepath.Join(root, prefix), "--cache", filepath.Join(root, "cache"), "--logs-dir", filepath.Join(root, "logs")}, extra...)
		command := exec.Command(npmExecutable(), args...)
		command.Env = append(os.Environ(), "TMPDIR="+filepath.Join(root, "tmp"), "TEMP="+filepath.Join(root, "tmp"), "TMP="+filepath.Join(root, "tmp"), "npm_config_update_notifier=false")
		return command
	}
	failures := &Failures{run: r}

	// 1. Install.
	install, err := Step(dir, "install", nil, npm("install", "prefix", tarball))
	if err != nil {
		return err
	}
	if install.Exit != 0 {
		failures.Addf("npm install exited %d", install.Exit)
	}
	command := installedCommand(root, "prefix")
	// 2. The installed command.
	if _, err := os.Stat(command); err != nil {
		failures.Addf("the installed command is missing at %s", command)
	} else {
		version, err := Step(dir, "version", nil, exec.Command(command, "version"))
		if err != nil {
			return err
		}
		if !version.HasLine("sandboxed-agents "+r.Version) || !version.HasLine("asset-sha256 "+r.AssetSHA256) {
			failures.Addf("version does not print the preview version and asset hash")
		}
		check, err := Step(dir, "check", nil, exec.Command(command, "check"))
		if err != nil {
			return err
		}
		if check.Exit != 0 {
			failures.Addf("check exited %d", check.Exit)
		}
		list, err := Step(dir, "list", nil, exec.Command(command, "list"))
		if err != nil {
			return err
		}
		if list.Exit != 0 || len(ListNames(list.Output())) != 0 {
			failures.Addf("list did not run without a sandbox (exit %d)", list.Exit)
		}
	}
	// 3. A tampered copy fails the checksum: one byte of the host's executable is changed.
	tampered := filepath.Join(root, "tampered.tgz")
	if err := tamperTarball(tarball, tampered, executableInTarball()); err != nil {
		return err
	}
	tamper, err := Step(dir, "install-tampered", nil, npm("install", "prefix2", tampered))
	if err != nil {
		return err
	}
	if tamper.Exit == 0 || !tamper.Contains("checksum mismatch") {
		failures.Addf("the tampered tarball did not fail on the checksum (exit %d)", tamper.Exit)
	}
	if _, err := os.Stat(installedCommand(root, "prefix2")); err == nil {
		failures.Addf("the tampered install left a command")
	}
	// 4. Uninstall.
	uninstall, err := Step(dir, "uninstall", nil, npm("uninstall", "prefix", "sandboxed-agents"))
	if err != nil {
		return err
	}
	if uninstall.Exit != 0 {
		failures.Addf("npm uninstall exited %d", uninstall.Exit)
	}
	if _, err := os.Stat(command); err == nil {
		failures.Addf("the command is still present after the uninstall")
	}
	r.Printf("install root %s holds the npm state of this run; remove it when the clean machine is reset\n", root)
	return failures.Finish(dir, "npm-install-clean", fmt.Sprintf("contained install printed version %s and the asset hash; check and list ran without a sandbox; the tampered copy failed on the checksum; the uninstall removed the command", r.Version))
}

// npmExecutable returns the npm command of the host.
func npmExecutable() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

// installedCommand returns the path of the command npm installs under a contained prefix.
func installedCommand(root, prefix string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, prefix, "sandboxed-agents.cmd")
	}
	return filepath.Join(root, prefix, "bin", "sandboxed-agents")
}

// executableInTarball returns the name of the host's executable inside the npm package.
func executableInTarball() string {
	if runtime.GOOS == "windows" {
		return "package/sandboxed-agents-windows-amd64.exe"
	}
	return "package/sandboxed-agents-linux-amd64"
}

// tamperTarball copies a gzipped tarball and changes one byte of the named member.
func tamperTarball(source, destination, member string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	unzip, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	zip := gzip.NewWriter(out)
	reader, writer := tar.NewReader(unzip), tar.NewWriter(zip)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if header.Name == member {
			if len(data) < 2 {
				return fmt.Errorf("%s is too small to tamper with", member)
			}
			data[len(data)/2] ^= 0xff
			found = true
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err := writer.Write(data); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("%s is not in the tarball", member)
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return zip.Close()
}

// members returns the names in a gzipped tarball, for tests and diagnostics.
func members(path string) ([]string, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	unzip, err := gzip.NewReader(in)
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(unzip)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		names = append(names, strings.TrimSuffix(header.Name, "/"))
	}
}
