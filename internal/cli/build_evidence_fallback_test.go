package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestUnconfirmedBuildCannotCompleteCoverage proves transient or mismatched confirmation leaves a fail-closed observation through the actual CLI, journal and production Cleanup seam, without changing the build outcome.
func TestUnconfirmedBuildCannotCompleteCoverage(t *testing.T) {
	for _, kind := range []string{"transient", "mismatch"} {
		for _, failed := range []bool{false, true} {
			t.Run(kind+"/failed="+strconv.FormatBool(failed), func(t *testing.T) {
				f := linuxHost(t)
				info := `{"host":{"hostname":"engine","security":{"rootless":true},"cgroupVersion":"v2"},"store":{"graphRoot":"/storage"}}`
				confirmation := response{Output: strings.Replace(info, `"engine"`, `"other"`, 1)}
				if kind == "transient" {
					confirmation = response{Stderr: "private engine diagnostic", Exit: 125}
				}
				f.responses["info --format json"] = response{Sequence: []response{{Output: info}, confirmation, {Output: info}}}
				directory := filepath.Join(t.TempDir(), "private", "build-evidence")
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(map[string]any{"Version": 2, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/storage"})
				if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(liverun.BuildEvidenceEnv, directory)
				id := "sha256:" + strings.Repeat("b", 64)
				tag := baseTag(t)
				asset := tag[len(tag)-64:]
				// An independent trusted final must remain eligible despite this fallback.
				if err := liverun.CaptureSingleCommitBuild(directory, tag, asset, false, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
					t.Fatal(err)
				}
				f.responses["image ls *"] = response{Output: `[]`}
				buildExit := 0
				if failed {
					buildExit = 125
				}
				f.responses["build *"] = response{Output: "original build output\n", Exit: buildExit}
				status, output, calls := f.run(t, "build")
				if (status != 0) != failed || !strings.Contains(output, "original build output") || strings.Contains(output, "private engine diagnostic") {
					t.Fatalf("supplement changed build: status=%d out=%s", status, output)
				}
				builds := 0
				for _, call := range calls {
					if len(call) > 0 && call[0] == "build" {
						builds++
						if slices.Contains(call, "--iidfile") || slices.Contains(call, "--layers=false") {
							t.Fatalf("unconfirmed fallback changed argv: %v", call)
						}
					}
				}
				if builds != 1 {
					t.Fatalf("build retried: %v", calls)
				}
				observations, _ := filepath.Glob(filepath.Join(directory, "unconfirmed-*.json"))
				if len(observations) != 1 {
					t.Fatalf("no private unconfirmed observation: %v", observations)
				}
				data, readErr := os.ReadFile(observations[0])
				var event struct {
					Version                        int
					Invocation, Revision, EngineID string
					Result                         liverun.BuildResult
					Unconfirmed                    bool
				}
				if readErr != nil || json.Unmarshal(data, &event) != nil || event.Version != 2 || event.Invocation != strings.Repeat("f", 64) || event.Revision != strings.Repeat("a", 40) || event.EngineID != "engine:/storage" || !event.Unconfirmed || event.Result != (liverun.BuildResult{}) {
					t.Fatalf("unbound or authorizing observation: %v %s", readErr, data)
				}
				engine, audit := collectEmittedCleanup(t, directory, id, "sha256:"+strings.Repeat("d", 64), asset)
				var report liverun.CleanupReport
				if err := json.Unmarshal([]byte(audit), &report); err != nil {
					t.Fatalf("no audit: %v %s", err, audit)
				}
				if report.Status != "unverified" || report.BuildPolicy != "single-commit-and-unverified" || len(report.Unverified) == 0 || len(engine.removed) != 1 || engine.removed[0] != id {
					t.Fatalf("fallback lost or independent proof disabled: removed=%v %s", engine.removed, audit)
				}
				unknownFound := false
				for _, skipped := range report.Skipped {
					if skipped.ID == "sha256:"+strings.Repeat("c", 64) {
						unknownFound = true
					}
					if skipped.ID == "sha256:"+strings.Repeat("c", 64) && skipped.Reason != "missing exact build proof" {
						t.Fatalf("unreceipted build falsely excluded: %+v", skipped)
					}
				}
				if !unknownFound {
					t.Fatal("audit omitted unknown image")
				}
				if strings.Contains(audit, "private engine diagnostic") {
					t.Fatalf("diagnostic leaked: %s", audit)
				}
			})
		}
	}
}
