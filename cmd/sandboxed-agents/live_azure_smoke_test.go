package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestLiveAzureSmokeRejectsBrokenCommands proves the live image smoke script rejects Azure CLI failures and missing DevOps groups at its shell/process seam without a live host.
func TestLiveAzureSmokeRejectsBrokenCommands(t *testing.T) {
	if os.Getenv(nestedGuardEnv) == strconv.Itoa(os.Getppid()) {
		t.Skip("the outer run covers the smoke shell; the nested guard deliberately excludes shell utilities from PATH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the image smoke script runs in a Linux sandbox")
	}
	for _, program := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Fatalf("offline smoke harness requires %s: %v", program, err)
		}
	}
	for _, failure := range []string{"", "version-exit", "module-error", "empty-version", "monitor", "postgres", "mysql", "extension", "devops"} {
		t.Run("failure="+failure, func(t *testing.T) {
			directory := t.TempDir()
			stub := `#!/bin/bash
set -eu
case "$*" in
  version)
    if [ "$FAILURE" = version-exit ]; then exit 7; fi
    if [ "$FAILURE" = module-error ]; then echo "Error loading command module 'monitor'" >&2; fi
    if [ "$FAILURE" = empty-version ]; then echo '{"azure-cli":""}'; else echo '{"azure-cli":"2.91.0","extensions":{"azure-devops":"1.0.10"}}'; fi ;;
  'monitor --help'|'postgres --help'|'mysql --help')
    if [ "$FAILURE" = "$1" ]; then exit 8; fi ;;
  'extension list')
    if [ "$FAILURE" = extension ]; then echo '[]'; else echo '[{"name":"azure-devops","version":"1.0.10"}]'; fi ;;
  'devops --help')
    if [ "$FAILURE" = devops ]; then echo 'no groups'; else printf 'Subgroups:\n    project : Projects.\n    team : Teams.\n    user : Users.\n'; fi ;;
  *) echo "unexpected az command: $*" >&2; exit 9 ;;
esac
`
			if err := os.WriteFile(filepath.Join(directory, "az"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("bash", "-c", "set -euo pipefail\n"+liveImageSmoke("azure"))
			command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "FAILURE="+failure, "TMPDIR="+directory)
			output, err := command.CombinedOutput()
			if (err != nil) != (failure != "") {
				t.Fatalf("smoke failure=%q: error=%v\n%s", failure, err, output)
			}
			if failure == "" && !strings.Contains(string(output), "1.0.10") {
				t.Fatalf("smoke omitted extension version: %s", output)
			}
		})
	}
}
