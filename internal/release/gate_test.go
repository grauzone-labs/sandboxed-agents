package release_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/release"
)

const gateCommit = "0123456789abcdef0123456789abcdef01234567"

func validatedEvidence() release.StableGateEvidence {
	records := map[string][]byte{}
	for _, platform := range []string{"linux", "windows-11"} {
		records["live-suite-"+platform+".json"] = []byte(`{"schema_version":2,"kind":"live-suite","commit":"` + gateCommit + `","platform":"` + platform + `","result":"pass","image_part_selected":true,"image_part_ran":true,"image_coverage_complete":true,"checks":[{"name":"image-build","result":"pass"}]}`)
	}
	records["manual-checklist.json"] = []byte(`{"schema_version":1,"kind":"manual-checklist","commit":"` + gateCommit + `","result":"pass","checks":[{"name":"agent-login","result":"pass","date":"2026-10-12","platform":"linux"}]}`)
	return release.StableGateEvidence{Commit: gateCommit, Preview: &release.PreviewEvidence{Commit: gateCommit, Records: records, AssetHash: strings.Repeat("a", 64)}, CICommit: gateCommit, CIGreen: true, AssetHash: strings.Repeat("a", 64)}
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
			if err := release.EvaluateStableGate(evidence); err == nil {
				t.Fatal("accepted missing live field")
			}
		})
	}
	for _, field := range []string{"image_part_ran", "image_coverage_complete"} {
		t.Run("live/false "+field, func(t *testing.T) {
			evidence := validatedEvidence()
			changeRecord(t, &evidence, "live-suite-linux.json", func(r map[string]any) { r[field] = false })
			if err := release.EvaluateStableGate(evidence); err == nil {
				t.Fatal("accepted incomplete image coverage")
			}
		})
	}
	for _, field := range []string{"date", "platform"} {
		t.Run("manual/missing "+field, func(t *testing.T) {
			evidence := validatedEvidence()
			changeRecord(t, &evidence, "manual-checklist.json", func(r map[string]any) { delete(r["checks"].([]any)[0].(map[string]any), field) })
			if err := release.EvaluateStableGate(evidence); err == nil {
				t.Fatal("accepted incomplete manual check")
			}
		})
	}
	for _, data := range []string{"{", "null", "{}", string(validatedEvidence().Preview.Records["live-suite-linux.json"]) + " {}"} {
		t.Run("malformed "+data[:1], func(t *testing.T) {
			evidence := validatedEvidence()
			evidence.Preview.Records["live-suite-linux.json"] = []byte(data)
			if err := release.EvaluateStableGate(evidence); err == nil {
				t.Fatal("accepted malformed live record")
			}
		})
	}
}

// TestStableGateRequiresPreview proves a release without a same-commit prerelease is refused through EvaluateStableGate.
func TestStableGateRequiresPreview(t *testing.T) {
	err := release.EvaluateStableGate(release.StableGateEvidence{Commit: gateCommit})
	if err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("error = %v; want named prerelease refusal", err)
	}
}

// TestStableGateRequiresSameCommit proves a prerelease on another commit is refused through EvaluateStableGate.
func TestStableGateRequiresSameCommit(t *testing.T) {
	err := release.EvaluateStableGate(release.StableGateEvidence{Commit: gateCommit, Preview: &release.PreviewEvidence{Commit: strings.Repeat("a", 40)}})
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

// TestStableGateAcceptsValidatedPreview proves a same-commit preview with valid records, green CI and the same asset hash passes through EvaluateStableGate.
func TestStableGateAcceptsValidatedPreview(t *testing.T) {
	if err := release.EvaluateStableGate(validatedEvidence()); err != nil {
		t.Fatal(err)
	}
}
