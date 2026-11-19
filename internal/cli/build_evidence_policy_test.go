package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestLiveBuildPolicyFlags proves the public CLI/fake-engine seam gates layer-free argv on selected-engine-confirmed evidence for serial and parallel builds on both targets.
func TestLiveBuildPolicyFlags(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, jobs := range []string{"1", "2"} {
			normalized := map[string][]string{}
			for _, channel := range []string{"active", "absent", "mismatch", "reproducible"} {
				t.Run(platform+"/"+jobs+"/"+channel, func(t *testing.T) {
					f := linuxHost(t)
					if platform == "windows" {
						f = windowsHost(t)
					}
					prefix := ""
					if platform == "windows" {
						prefix = "--connection chosen "
					}
					info := `{"host":{"hostname":"engine","security":{"rootless":true},"cgroupVersion":"v2"},"store":{"graphRoot":"/storage"}}`
					f.responses[prefix+"info --format json"] = response{Output: info}
					directory := t.TempDir()
					engine := "engine:/storage"
					if channel == "mismatch" {
						engine = "other:/storage"
					}
					data, _ := json.Marshal(map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": engine})
					if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv(liverun.BuildEvidenceEnv, "")
					if channel != "absent" {
						t.Setenv(liverun.BuildEvidenceEnv, directory)
					}
					if channel == "reproducible" {
						t.Setenv("SOURCE_DATE_EPOCH", "0")
					} else {
						t.Setenv("SOURCE_DATE_EPOCH", "")
						os.Unsetenv("SOURCE_DATE_EPOCH")
					}
					id := "sha256:" + strings.Repeat("b", 64)
					t.Setenv("TEST_PODMAN_IIDFILE_CONTENT", id)
					tag := baseTag(t)
					asset := tag[len(tag)-64:]
					f.responses[prefix+"image ls *"] = response{Output: `[]`}
					f.responses[prefix+"build *"] = response{}
					f.responses[prefix+"image inspect *"] = response{Output: `[{"Id":"` + id + `","Labels":{"io.sandboxed-agents.asset-sha256":"` + asset + `"}}]`}
					status, output, calls := f.run(t, "build", "--with", "dotnet", "--jobs", jobs)
					if status != 0 {
						t.Fatalf("build=%d %s calls=%v", status, output, calls)
					}
					normalized[channel] = nil
					builds := 0
					for _, call := range calls {
						if i := slices.Index(call, "build"); i >= 0 {
							builds++
							args := call[i+1:]
							clean := []string{}
							counts := map[string]int{}
							for j := 0; j < len(args); j++ {
								arg := args[j]
								counts[arg]++
								if arg == "--iidfile" {
									j++
									continue
								}
								if arg == "--layers=false" {
									continue
								}
								if j == 1 {
									arg = "<recipe>"
								}
								if j == len(args)-1 {
									arg = "<context>"
								}
								clean = append(clean, arg)
							}
							normalized[channel] = append(normalized[channel], strings.Join(clean, " "))
							if counts["--iidfile"] > 1 || counts["--layers=false"] > 1 {
								t.Fatalf("duplicate evidence flags: %v", call)
							}
							want := channel == "active" || channel == "reproducible"
							if slices.Contains(args, "--layers=false") != want || slices.Contains(args, "--iidfile") != want || slices.Contains(args, "--layer-label") {
								t.Fatalf("wrong policy argv: %v", call)
							}
							if !slices.Contains(args, "--no-cache") {
								t.Fatalf("lost sole rebuild policy: %v", call)
							}
							if want && (slices.Index(args, "--iidfile") >= len(args)-2 || slices.Index(args, "--layers=false") >= len(args)-1) {
								t.Fatalf("evidence flags after context: %v", call)
							}
							if platform == "windows" && (len(call) < 3 || call[0] != "--connection") {
								t.Fatalf("unbound Windows build: %v", call)
							}
						}
					}
					if channel == "active" || channel == "reproducible" {
						paths, _ := filepath.Glob(filepath.Join(directory, "result-*.json"))
						if len(paths) != 2 {
							t.Fatalf("missing invocation receipts: %v", paths)
						}
						for _, path := range paths {
							data, e := os.ReadFile(path)
							if e != nil {
								t.Fatal(e)
							}
							var event struct {
								Version int
								Result  struct{ Policy, Reproducibility, Outcome string }
							}
							if e = json.Unmarshal(data, &event); e != nil {
								t.Fatal(e)
							}
							expected := "default-timestamps"
							if channel == "reproducible" {
								expected = "reuse-not-excluded"
							}
							if event.Version != 2 || event.Result.Policy != "single-commit" || event.Result.Reproducibility != expected || event.Result.Outcome != "committed" {
								t.Fatalf("bad receipt: %s", data)
							}
						}
					}
					if builds != 2 {
						t.Fatalf("expected base plus toolchain via runner: %v", calls)
					}
				})
			}
			for _, channel := range []string{"active", "mismatch", "reproducible"} {
				if !slices.Equal(normalized[channel], normalized["absent"]) {
					t.Fatalf("non-evidence argv changed for %s/%s/%s: %v versus %v", platform, jobs, channel, normalized[channel], normalized["absent"])
				}
			}

		}
	}
}
