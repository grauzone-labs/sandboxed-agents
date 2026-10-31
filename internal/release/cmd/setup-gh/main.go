// Command setup-gh installs the checksum-pinned GitHub CLI that release publication and preview
// verification use, so that neither relies on the gh of the runner image. It downloads the pinned
// upstream archive, refuses it unless its SHA-256 is the pinned one, extracts it below RUNNER_TEMP,
// runs the installed gh once, and puts its bin directory first on the path of the following steps
// through GITHUB_PATH.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// pinnedArchive is the one gh archive the release workflows accept: the upstream release, its file
// name, and the SHA-256 of that file from the release's checksum list, compared on 2026-10-18.
// Changing the pin means changing previewVerifierVersion in internal/release/cmd/stable-gate too.
var pinnedArchive = archive{
	Version: "2.102.0",
	URL:     "https://github.com/cli/cli/releases/download/v2.102.0/gh_2.102.0_linux_amd64.tar.gz",
	SHA256:  "bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386",
}

// archive names one gh release archive: its version, where it is downloaded from, and the SHA-256
// its bytes must have.
type archive struct {
	Version, URL, SHA256 string
}

// directoryName is the top-level directory of the gh archive, which holds bin/gh.
func (a archive) directoryName() string { return "gh_" + a.Version + "_linux_amd64" }

// main installs the pinned gh for the running Actions job and exits with the result.
func main() {
	if err := install(pinnedArchive, download, os.Getenv("RUNNER_TEMP"), os.Getenv("GITHUB_PATH"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "setup-gh:", err)
		os.Exit(1)
	}
}

// download fetches a URL and returns its whole body, following redirects as GitHub's release
// downloads require.
func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	return io.ReadAll(response.Body)
}

// install downloads the archive with fetch, refuses any bytes whose SHA-256 differs from the pin
// before writing anything, extracts the archive into RUNNER_TEMP/release-tools, runs the installed
// gh with --version on stdout, and appends its bin directory to the GITHUB_PATH file. It requires
// RUNNER_TEMP and GITHUB_PATH.
func install(pin archive, fetch func(string) ([]byte, error), runnerTemp, githubPath string, stdout io.Writer) error {
	if runnerTemp == "" || githubPath == "" {
		return errors.New("RUNNER_TEMP and GITHUB_PATH are required")
	}
	data, err := fetch(pin.URL)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != pin.SHA256 {
		return fmt.Errorf("%s: SHA-256 %s differs from the pinned %s; nothing installed", filepath.Base(pin.URL), got, pin.SHA256)
	}
	directory := filepath.Join(runnerTemp, "release-tools")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	if err := extract(data, directory); err != nil {
		return err
	}
	bin := filepath.Join(directory, pin.directoryName(), "bin")
	version := exec.Command(filepath.Join(bin, "gh"), "--version")
	version.Stdout, version.Stderr = stdout, os.Stderr
	if err := version.Run(); err != nil {
		return fmt.Errorf("installed gh: %w", err)
	}
	file, err := os.OpenFile(githubPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(bin + "\n"); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// extract unpacks a gzip-compressed tar archive below destination, with the modes the archive
// records, and refuses any entry that is not a plain file or directory below the destination.
func extract(data []byte, destination string) error {
	decompressed, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer decompressed.Close()
	root := filepath.Clean(destination) + string(filepath.Separator)
	reader := tar.NewReader(decompressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		if !strings.HasPrefix(target+string(filepath.Separator), root) {
			return fmt.Errorf("archive entry escapes the destination: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, reader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %s has unsupported type %q", header.Name, header.Typeflag)
		}
	}
}
