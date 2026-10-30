package manualcheck_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/manualcheck"
)

// TestRecordIdentifiersMatchChecklist proves the runner's 26 identifiers are exactly the rows of
// the "Check identifiers" table in docs/manual-checklist.md, in order, with the same fixed
// platforms, through the real document and the exported list.
func TestRecordIdentifiersMatchChecklist(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "manual-checklist.md"))
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("^\\| `([a-z0-9-]+)` \\| \\[[^\\]]+\\]\\([^)]+\\) \\| `?([a-z0-9-]+)`? \\|$")
	var documented []string
	platforms := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if match := row.FindStringSubmatch(line); match != nil {
			documented = append(documented, match[1])
			platforms[match[1]] = match[2]
		}
	}
	if !slices.Equal(documented, manualcheck.Identifiers) {
		t.Fatalf("documented identifiers %v differ from the runner's %v", documented, manualcheck.Identifiers)
	}
	if len(documented) != 26 {
		t.Fatalf("want 26 identifiers, got %d", len(documented))
	}
	for name, platform := range platforms {
		fixed := manualcheck.PlatformOf(name)
		if platform == "either" {
			platform = ""
		}
		if fixed != platform {
			t.Errorf("%s: document says platform %q, runner says %q", name, platform, fixed)
		}
	}
}

// TestRecordValidateRules proves Validate applies every rule of "The record": the fields, the
// commit form, the identifiers, duplicates, the fixed platforms, the date form, fail on any
// failed check, and pass only with all 26 passed, through LoadRecord on real files.
func TestRecordValidateRules(t *testing.T) {
	commit := strings.Repeat("a", 40)
	write := func(t *testing.T, text string) string {
		path := filepath.Join(t.TempDir(), "manual-checklist.json")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	check := `{"name":"github-login","result":"pass","date":"2026-10-22","platform":"linux"}`
	valid := `{"schema_version":1,"kind":"manual-checklist","commit":"` + commit + `","result":"fail","checks":[` + check + `]}`
	if record, err := manualcheck.LoadRecord(write(t, valid)); err != nil {
		t.Fatal(err)
	} else if err := record.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	for name, text := range map[string]string{
		"extra field":          strings.Replace(valid, `"result":"fail"`, `"result":"fail","note":"x"`, 1),
		"extra check field":    strings.Replace(valid, `"platform":"linux"}`, `"platform":"linux","host":"h"}`, 1),
		"missing field":        strings.Replace(valid, `"kind":"manual-checklist",`, ``, 1),
		"uppercase commit":     strings.Replace(valid, commit, strings.Repeat("A", 40), 1),
		"unknown identifier":   strings.Replace(valid, "github-login", "agent-login", 1),
		"duplicate identifier": strings.Replace(valid, check, check+","+check, 1),
		"wrong fixed platform": strings.Replace(valid, "github-login", "nuget-install-clean", 1),
		"bad date":             strings.Replace(valid, "2026-10-22", "22.10.2026", 1),
		"pass with failure":    strings.Replace(strings.Replace(valid, `"result":"fail"`, `"result":"pass"`, 1), `"result":"pass","date"`, `"result":"fail","date"`, 1),
		"pass incomplete":      strings.Replace(valid, `"result":"fail"`, `"result":"pass"`, 1),
		"empty checks":         strings.Replace(valid, "["+check+"]", "[]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			record, err := manualcheck.LoadRecord(write(t, text))
			if err != nil {
				return
			}
			if err := record.Validate(); err == nil {
				t.Fatalf("record validated: %s", text)
			}
		})
	}
}

// TestRecordAddCheckOwnsThePass proves AddCheck records a pass only with the human-observed flag,
// refuses an unknown identifier, a wrong fixed platform, and a silent overwrite, and turns the
// result to pass only when all 26 identifiers passed, through the real Record functions and an
// on-disk round trip.
func TestRecordAddCheckOwnsThePass(t *testing.T) {
	record, err := manualcheck.NewRecord(strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	pass := manualcheck.Check{Name: "github-login", Result: "pass", Date: "2026-10-22", Platform: "linux"}
	if err := record.AddCheck(pass, false, false); err == nil {
		t.Fatal("a pass was recorded without the human-observed flag")
	}
	if err := record.AddCheck(pass, false, true); err != nil {
		t.Fatal(err)
	}
	if err := record.AddCheck(pass, false, true); err == nil {
		t.Fatal("an identifier was overwritten without replace")
	}
	if err := record.AddCheck(manualcheck.Check{Name: "github-login", Result: "fail", Date: "2026-10-23", Platform: "linux"}, true, false); err != nil {
		t.Fatal(err)
	}
	if record.Result != "fail" || len(record.Checks) != 1 || record.Checks[0].Result != "fail" {
		t.Fatalf("replace did not record the rerun: %+v", record)
	}
	if err := record.AddCheck(manualcheck.Check{Name: "quick-guide-linux", Result: "pass", Date: "2026-10-22", Platform: "windows-11"}, false, true); err == nil {
		t.Fatal("a wrong fixed platform was accepted")
	}
	if err := record.AddCheck(manualcheck.Check{Name: "agent-login", Result: "fail", Date: "2026-10-22", Platform: "linux"}, false, false); err == nil {
		t.Fatal("an unknown identifier was accepted")
	}
	for _, name := range manualcheck.Identifiers {
		platform := manualcheck.PlatformOf(name)
		if platform == "" {
			platform = "linux"
		}
		if err := record.AddCheck(manualcheck.Check{Name: name, Result: "pass", Date: "2026-10-22", Platform: platform}, true, true); err != nil {
			t.Fatal(err)
		}
	}
	if record.Result != "pass" {
		t.Fatalf("all 26 passed but result is %s", record.Result)
	}
	path := filepath.Join(t.TempDir(), "manual-checklist.json")
	if err := record.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := manualcheck.LoadRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(); err != nil || !strings.Contains(loaded.Status(), "recorded 26 of 26 identifiers; 0 not run; 0 failed") {
		t.Fatalf("round trip: %v\n%s", err, loaded.Status())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIsUnix() && info.Mode().Perm() != 0600 {
		t.Fatalf("record mode %v; want 0600", info.Mode().Perm())
	}
}

// TestRecordDocumentedValidatorIsTheRunnersRules proves the jq program printed in the checklist and
// the runner's Validate agree on a passing and on a failing record, through the real jq when it is
// installed.
func TestRecordDocumentedValidatorIsTheRunnersRules(t *testing.T) {
	jq, err := lookJq()
	if err != nil {
		t.Skip("jq is not installed")
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "manual-checklist.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	start := strings.Index(text, "IDS='[")
	if start < 0 {
		t.Fatal("the documented jq validator was not found")
	}
	end := strings.Index(text[start:], "' manual-checklist.json")
	if end < 0 {
		t.Fatal("the documented jq validator was not found")
	}
	script := text[start : start+end+len("' manual-checklist.json")]
	record, _ := manualcheck.NewRecord(strings.Repeat("a", 40))
	_ = record.AddCheck(manualcheck.Check{Name: "github-login", Result: "fail", Date: "2026-10-22", Platform: "linux"}, false, false)
	for name, mutate := range map[string]func(*manualcheck.Record){
		"in progress":  func(*manualcheck.Record) {},
		"pass claimed": func(r *manualcheck.Record) { r.Result = "pass" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := *record
			copy.Checks = slices.Clone(record.Checks)
			mutate(&copy)
			dir := t.TempDir()
			if err := copy.Save(filepath.Join(dir, "manual-checklist.json")); err != nil {
				t.Fatal(err)
			}
			documented := runShell(t, dir, jq, script) == nil
			runner := copy.Validate() == nil
			if documented != runner {
				t.Fatalf("documented validator says %t, runner says %t", documented, runner)
			}
		})
	}
}
