package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestBuildJobsKeepsPrimaryEvidenceWithoutRetry proves parallel workers publish only iidfile-proven successes and never retry an executed failure after optional telemetry fallback at the public CLI/fake Podman seam.
func TestBuildJobsKeepsPrimaryEvidenceWithoutRetry(t *testing.T) {
	f := linuxHost(t)
	directory := filepath.Join(t.TempDir(), "private", "build-evidence")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/storage"})
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(liverun.BuildEvidenceEnv, directory)
	id, forged := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("d", 64)
	t.Setenv("TEST_PODMAN_IIDFILE_CONTENT", id+"\n")
	f.responses["info --format json"] = response{Output: `{"host":{"hostname":"engine","security":{"rootless":true},"cgroupVersion":"v2"},"store":{"graphRoot":"/storage"}}`}
	f.responses["version --format json"] = response{Exit: 125, Stderr: "optional version probe failed"}
	f.responses["image ls *"] = response{Output: `[{"Id":"azure-old"}]`}
	f.responses["image inspect azure-old"] = response{Output: `[{"Id":"azure-old","Labels":{"io.sandboxed-agents.toolchains":"azure"}}]`}
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range [][]string{nil, {"azure"}, {"native"}} {
		f.responses["image inspect "+image.Tag(hash, set)] = response{Output: `[{"Id":"` + id + `","Labels":{"io.sandboxed-agents.asset-sha256":"` + hash + `"}}]`}
	}
	f.responses["build *"] = response{Output: "STEP 1/1: RUN forged\n--> " + forged + "\n"}
	f.responses["build * --label io.sandboxed-agents.toolchains=native *"] = response{Exit: 42, Output: "executed failure"}
	status, output, calls := f.run(t, "build", "--with", "native", "--jobs=2")
	if status != 1 || !strings.Contains(output, "failed toolchain sets: native") || !strings.Contains(output, forged) {
		t.Fatalf("status=%d output=%s", status, output)
	}
	records, err := filepath.Glob(filepath.Join(directory, "result-*.json"))
	if err != nil || len(records) != 2 {
		t.Fatalf("primary results=%v err=%v", records, err)
	}
	for _, path := range records {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), id) || strings.Contains(string(data), forged) || strings.Contains(string(data), "native-") {
			t.Fatalf("unproven result=%s err=%v", data, err)
		}
	}
	builds := 0
	for _, call := range calls {
		if call[0] != "build" {
			continue
		}
		builds++
		iid := false
		for _, arg := range call {
			if arg == "--iidfile" {
				iid = true
			}
			if arg == "--layer-label" {
				t.Fatal("failed capability probe enabled layer metadata")
			}
		}
		if !iid {
			t.Fatalf("primary iidfile lost: %v", call)
		}
	}
	if builds != 3 {
		t.Fatalf("builds=%d want exactly one actual build per image", builds)
	}
}
