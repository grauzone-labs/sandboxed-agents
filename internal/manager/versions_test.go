package manager_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// versionsEffects supplies root identity, fixed files, the given programs on PATH, and process starts
// that print a line naming the program and its arguments, and records every start and written file.
func versionsEffects(t *testing.T, present []string, started *[]manager.Process, written map[string][]byte) manager.Effects {
	t.Helper()
	effects := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	effects.ReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/etc/os-release":
			return []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n"), nil
		case "/usr/local/share/sandboxed-agents/azure-devops-requirements.txt":
			return []byte("keyring==25.6.0 --hash=sha256:abc\n"), nil
		}
		return nil, fmt.Errorf("unexpected read of %s", path)
	}
	effects.LookPath = func(program string) (string, error) {
		for _, name := range present {
			if name == program {
				return "/usr/bin/" + program, nil
			}
		}
		return "", errors.New("not found")
	}
	effects.Start = func(process manager.Process) error {
		*started = append(*started, process)
		if process.Identity != (manager.Identity{UID: 0, GID: 0}) {
			t.Errorf("probe %s ran as %+v; want root", process.Program, process.Identity)
		}
		if process.Program == "missing" {
			return errors.New("exec: not found")
		}
		fmt.Fprintf(process.Stdout, "%s %s\n", process.Program, strings.Join(process.Args, " "))
		return nil
	}
	effects.WriteFile = func(path string, data []byte, mode os.FileMode) error {
		if mode != 0644 {
			t.Errorf("manifest mode %v; want 0644", mode)
		}
		written[path] = data
		return nil
	}
	return effects
}

// TestRecordVersionsWritesManifestWithPresentToolchains proves record-versions, run as root, records the OS release,
// the packages, Node and npm, its own version line, every toolchain whose program is on PATH, the Azure requirements,
// and removes root's Azure state, into the shared manifest, through the manager command and injected OS seams.
func TestRecordVersionsWritesManifestWithPresentToolchains(t *testing.T) {
	var started []manager.Process
	written := map[string][]byte{}
	var stdout, stderr bytes.Buffer
	status := manager.RunWithEffects([]string{"record-versions"}, &stdout, &stderr, versionsEffects(t, []string{"dotnet", "az"}, &started, written))
	if status != 0 || stderr.Len() != 0 || stdout.Len() != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q; want a silent success", status, stdout.String(), stderr.String())
	}
	want := "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n" +
		"dpkg-query -W -f=${Package}=${Version}\n\n" +
		"node --version\n" +
		"npm --version\n" +
		"sandboxed-agents-manager development\n" +
		"dotnet --list-sdks\n" +
		"az version\n" +
		"az extension list\n" +
		"keyring==25.6.0 --hash=sha256:abc\n" +
		"rm -rf /root/.azure\n"
	if got := string(written["/usr/local/share/sandboxed-agents/versions.txt"]); got != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s", got, want)
	}
	var programs []string
	for _, process := range started {
		programs = append(programs, process.Program)
		if process.Program == "az" && !reflect.DeepEqual(process.Environment, []string{"HOME=/root", "AZURE_CONFIG_DIR=/root/.azure"}) {
			t.Errorf("az environment = %q; want root's home and Azure directory", process.Environment)
		}
	}
	if !reflect.DeepEqual(programs, []string{"dpkg-query", "node", "npm", "dotnet", "az", "az", "rm"}) {
		t.Fatalf("programs = %q; want the required probes, the present toolchains, and the Azure cleanup", programs)
	}
}

// TestRecordVersionsSkipsAbsentToolchainsAndAzureCleanup proves an image without toolchains records only the
// required probes, with no Azure requirements or cleanup, through the manager command and injected OS seams.
func TestRecordVersionsSkipsAbsentToolchainsAndAzureCleanup(t *testing.T) {
	var started []manager.Process
	written := map[string][]byte{}
	var stdout, stderr bytes.Buffer
	if status := manager.RunWithEffects([]string{"record-versions"}, &stdout, &stderr, versionsEffects(t, nil, &started, written)); status != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
	manifest := string(written["/usr/local/share/sandboxed-agents/versions.txt"])
	if len(started) != 3 || strings.Contains(manifest, "keyring") || strings.Contains(manifest, "rm -rf") || !strings.HasSuffix(manifest, "npm --version\nsandboxed-agents-manager development\n") {
		t.Fatalf("starts=%d manifest=%q; want the three required probes and the version line only", len(started), manifest)
	}
}

// TestRecordVersionsRefusesAgentAndFailedProbes proves record-versions refuses the agent identity before any process
// and reports a required probe that cannot start without writing the manifest, through the manager command seam.
func TestRecordVersionsRefusesAgentAndFailedProbes(t *testing.T) {
	var started []manager.Process
	written := map[string][]byte{}
	var stdout, stderr bytes.Buffer
	effects := versionsEffects(t, nil, &started, written)
	effects.Identity = func() manager.Identity { return manager.Identity{UID: 1000, GID: 1000} }
	if status := manager.RunWithEffects([]string{"record-versions"}, &stdout, &stderr, effects); status != 1 || len(started) != 0 || !strings.Contains(stderr.String(), "runs as root") {
		t.Fatalf("status=%d starts=%d stderr=%q; want a root refusal before any process", status, len(started), stderr.String())
	}
	stderr.Reset()
	effects = versionsEffects(t, nil, &started, written)
	effects.Start = func(process manager.Process) error {
		started = append(started, process)
		return errors.New("exec: not found")
	}
	if status := manager.RunWithEffects([]string{"record-versions"}, &stdout, &stderr, effects); status != 1 || len(written) != 0 || !strings.Contains(stderr.String(), "dpkg-query") {
		t.Fatalf("status=%d written=%d stderr=%q; want the failed probe named and no manifest", status, len(written), stderr.String())
	}
}
