package release_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

const gateCommit = "0123456789abcdef0123456789abcdef01234567"

// validatedEvidence returns matching preview, validation records, CI, and asset hashes for a passing gate.
func validatedEvidence() release.StableGateEvidence {
	records := map[string][]byte{}
	for _, platform := range []string{"linux", "windows-11"} {
		records["live-suite-"+platform+".json"] = []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + gateCommit + `","platform":"` + platform + `","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[{"name":"image-build","result":"pass"}]}`)
	}
	records["manual-checklist.json"] = []byte(`{"schema_version":1,"kind":"manual-checklist","commit":"` + gateCommit + `","result":"pass","checks":[{"name":"agent-login","result":"pass","date":"2026-10-12","platform":"linux"}]}`)
	return release.StableGateEvidence{Tag: "v1.0.0", Commit: gateCommit, Preview: &release.PreviewEvidence{Tag: "v1.0.0-preview.20261012.1", Commit: gateCommit, Records: records, AssetHash: strings.Repeat("a", 64)}, CICommit: gateCommit, CIGreen: true, AssetHash: strings.Repeat("a", 64)}
}

// TestStableGateRequiresValidationFiles proves all three exact validation filenames are mandatory through EvaluateStableGate.
func TestStableGateRequiresValidationFiles(t *testing.T) {
	for _, filename := range []string{"live-suite-linux.json", "live-suite-windows-11.json", "manual-checklist.json"} {
		t.Run(filename, func(t *testing.T) {
			evidence := validatedEvidence()
			delete(evidence.Preview.Records, filename)
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), filename) {
				t.Fatalf("error = %v; want named missing file", err)
			}
		})
	}
}

// changeRecord decodes a fixture validation record, applies change, and stores its updated JSON.
func changeRecord(t *testing.T, evidence *release.StableGateEvidence, filename string, change func(map[string]any)) {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(evidence.Preview.Records[filename], &record); err != nil {
		t.Fatal(err)
	}
	change(record)
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Preview.Records[filename] = data
}

// TestStableGateValidatesRecords proves failed, wrong-commit, incomplete and malformed live/manual records are refused by filename through EvaluateStableGate.
func TestStableGateValidatesRecords(t *testing.T) {
	mutations := []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong commit", func(r map[string]any) { r["commit"] = strings.Repeat("b", 40) }},
		{"failure", func(r map[string]any) { r["result"] = "fail" }},
		{"missing result", func(r map[string]any) { delete(r, "result") }},
		{"wrong schema", func(r map[string]any) { r["schema_version"] = 0 }},
		{"wrong kind", func(r map[string]any) { r["kind"] = "other" }},
		{"additional field", func(r map[string]any) { r["host"] = "secret-host" }},
		{"additional check field", func(r map[string]any) { r["checks"].([]any)[0].(map[string]any)["token"] = "secret" }},
		{"missing check name", func(r map[string]any) { delete(r["checks"].([]any)[0].(map[string]any), "name") }},
		{"missing check result", func(r map[string]any) { delete(r["checks"].([]any)[0].(map[string]any), "result") }},
		{"wrong checks type", func(r map[string]any) { r["checks"] = "pass" }},
	}
	for _, filename := range []string{"live-suite-linux.json", "live-suite-windows-11.json", "manual-checklist.json"} {
		for _, mutation := range mutations {
			t.Run(filename+"/"+mutation.name, func(t *testing.T) {
				evidence := validatedEvidence()
				changeRecord(t, &evidence, filename, mutation.change)
				if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), filename) {
					t.Fatalf("error = %v; want named record refusal", err)
				}
			})
		}
	}
	for _, field := range []string{"image_part_selected", "image_part_ran", "image_coverage_complete", "platform"} {
		t.Run("live/missing "+field, func(t *testing.T) {
			evidence := validatedEvidence()
			changeRecord(t, &evidence, "live-suite-linux.json", func(r map[string]any) { delete(r, field) })
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "live-suite-linux.json") {
				t.Fatalf("error=%v; want named missing live field", err)
			}
		})
	}
	for _, field := range []string{"image_part_ran", "image_coverage_complete"} {
		t.Run("live/false "+field, func(t *testing.T) {
			evidence := validatedEvidence()
			changeRecord(t, &evidence, "live-suite-linux.json", func(r map[string]any) { r[field] = false })
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "live-suite-linux.json") {
				t.Fatalf("error=%v; want named incomplete image coverage", err)
			}
		})
	}
	for _, field := range []string{"date", "platform"} {
		t.Run("manual/missing "+field, func(t *testing.T) {
			evidence := validatedEvidence()
			changeRecord(t, &evidence, "manual-checklist.json", func(r map[string]any) { delete(r["checks"].([]any)[0].(map[string]any), field) })
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "manual-checklist.json") {
				t.Fatalf("error=%v; want named incomplete manual check", err)
			}
		})
	}
	for _, data := range []string{"{", "null", "{}", string(validatedEvidence().Preview.Records["live-suite-linux.json"]) + " {}"} {
		t.Run("malformed "+data[:1], func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Preview.Records["live-suite-linux.json"] = []byte(data)
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "live-suite-linux.json") {
				t.Fatalf("error=%v; want named malformed live record", err)
			}
		})
	}
}

// TestStableGateRequiresPreview proves a release without a same-commit prerelease is refused through EvaluateStableGate.
func TestStableGateRequiresPreview(t *testing.T) {
	err := release.EvaluateStableGate(release.StableGateEvidence{Tag: "v1.0.0", Commit: gateCommit})
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("error = %v; want named prerelease refusal", err)
	}
}

// TestStableGateRequiresSameCommit proves a prerelease on another commit is refused through EvaluateStableGate.
func TestStableGateRequiresSameCommit(t *testing.T) {
	err := release.EvaluateStableGate(release.StableGateEvidence{Tag: "v1.0.0", Commit: gateCommit, Preview: &release.PreviewEvidence{Tag: "v1.0.0-preview.20261012.1", Commit: strings.Repeat("a", 40)}})
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("error = %v; want named prerelease commit refusal", err)
	}
}

// TestStableGateRequiresGreenCI proves only green CI on the release commit passes through EvaluateStableGate.
func TestStableGateRequiresGreenCI(t *testing.T) {
	for _, name := range []string{"failed", "other commit"} {
		t.Run(name, func(t *testing.T) {
			evidence := validatedEvidence()
			if name == "failed" {
				evidence.CIGreen = false
			} else {
				evidence.CICommit = strings.Repeat("b", 40)
			}
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "CI") {
				t.Fatalf("error=%v; want named CI refusal", err)
			}
		})
	}
}

// TestStableGateRequiresSameAssetHash proves preview/stable hash differences or missing hashes refuse publication through EvaluateStableGate.
func TestStableGateRequiresSameAssetHash(t *testing.T) {
	for _, hash := range []string{"", strings.Repeat("b", 64), "invalid"} {
		t.Run(hash, func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.AssetHash = hash
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "asset hash") {
				t.Fatalf("error=%v; want named asset hash refusal", err)
			}
		})
	}
}

// TestStableGateAcceptsValidatedPreview proves a same-version and same-commit preview with valid records, green CI and the same asset hash passes through EvaluateStableGate.
func TestStableGateAcceptsValidatedPreview(t *testing.T) {
	for _, tags := range []struct{ stable, preview string }{
		{"v1.0.0", "v1.0.0-preview.20261012.1"},
		{"v2.0.0", "v2.0.0-preview.20261012.2"},
	} {
		t.Run(tags.stable, func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Tag = tags.stable
			evidence.Preview.Tag = tags.preview
			if err := release.EvaluateStableGate(evidence); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestStableGateRejectsDuplicateValidationKeys proves repeated keys in records and nested checks cannot overwrite a failure through EvaluateStableGate and name the rejected file.
func TestStableGateRejectsDuplicateValidationKeys(t *testing.T) {
	for _, filename := range []string{"live-suite-linux.json", "live-suite-windows-11.json", "manual-checklist.json"} {
		for _, duplicate := range []struct{ name, find, replacement string }{
			{"overall result", "\"result\":\"pass\"", "\"result\":\"fail\",\"result\":\"pass\""},
			{"nested check result", "\"checks\":[{", "\"checks\":[{\"result\":\"fail\","},
			{"escaped duplicate name", "\"checks\":[{", "\"checks\":[{\"\\u006eame\":\"other\","},
		} {
			t.Run(filename+"/"+duplicate.name, func(t *testing.T) {
				evidence := validatedEvidence()
				original := string(evidence.Preview.Records[filename])
				changed := strings.Replace(original, duplicate.find, duplicate.replacement, 1)
				if changed == original {
					t.Fatal("duplicate-key fixture did not change")
				}
				evidence.Preview.Records[filename] = []byte(changed)
				err := release.EvaluateStableGate(evidence)
				if err == nil || !strings.Contains(err.Error(), filename) || !strings.Contains(err.Error(), "duplicate") {
					t.Fatalf("error=%v; want named duplicate-key refusal", err)
				}
			})
		}
	}
}

// TestStableGateRejectsEmptyManualChecklist proves a passing overall result cannot validate a checklist with no checks through EvaluateStableGate and names manual-checklist.json.
func TestStableGateRejectsEmptyManualChecklist(t *testing.T) {
	evidence := validatedEvidence()
	changeRecord(t, &evidence, "manual-checklist.json", func(record map[string]any) { record["checks"] = []any{} })
	err := release.EvaluateStableGate(evidence)
	if err == nil || !strings.Contains(err.Error(), "manual-checklist.json") {
		t.Fatalf("error=%v; want named empty-checklist refusal", err)
	}
}

// TestStableGateRejectsMismatchedLivePlatform proves a Windows live record naming Linux is refused by its Windows filename through EvaluateStableGate.
func TestStableGateRejectsMismatchedLivePlatform(t *testing.T) {
	evidence := validatedEvidence()
	changeRecord(t, &evidence, "live-suite-windows-11.json", func(record map[string]any) { record["platform"] = "linux" })
	err := release.EvaluateStableGate(evidence)
	if err == nil || !strings.Contains(err.Error(), "live-suite-windows-11.json") || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("error=%v; want named Windows platform refusal", err)
	}
}

// TestStableGateRequiresMatchingPreviewVersion proves a fully validated same-commit preview must have the stable tag's X.Y.Z through EvaluateStableGate.
func TestStableGateRequiresMatchingPreviewVersion(t *testing.T) {
	for _, tag := range []string{"v2.0.0-preview.20261012.1", "v1.1.0-preview.20261012.1", "v1.0.1-preview.20261012.1", "v1.0.00-preview.20261012.1"} {
		t.Run(tag, func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Preview.Tag = tag
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "version") {
				t.Fatalf("error=%v; want named preview version refusal", err)
			}
		})
	}
}

// TestStableGateRequiresWellFormedTags proves stable and preview tag syntax is validated independently of passing records through EvaluateStableGate.
func TestStableGateRequiresWellFormedTags(t *testing.T) {
	for _, tag := range []string{"", "1.0.0", "v1.0", "v1.0.0-preview.20261012.1"} {
		t.Run("stable/"+tag, func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Tag = tag
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "stable tag") {
				t.Fatalf("error=%v; want named stable tag refusal", err)
			}
		})
	}
	for _, tag := range []string{"", "v1.0.0", "v1.0.0-preview.2026101.1", "v1.0.0-preview.20261012.1-extra"} {
		t.Run("preview/"+tag, func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Preview.Tag = tag
			if err := release.EvaluateStableGate(evidence); err == nil || !strings.Contains(err.Error(), "version") {
				t.Fatalf("error=%v; want named preview version refusal", err)
			}
		})
	}
}
