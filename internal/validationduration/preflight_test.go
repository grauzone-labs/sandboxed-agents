package validationduration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestRetainPreflightMeasuresSeparateScopes proves actual process observations
// retain separate preflight/selection totals, idempotence and unchanged schema2
// at the public journal-to-Retain file seam.
func TestRetainPreflightMeasuresSeparateScopes(t *testing.T) {
	directory := t.TempDir()
	revision := "0123456789abcdef0123456789abcdef01234567"
	channel, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(channel, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, scope := range []preflighttiming.Scope{preflighttiming.Preflight, preflighttiming.TargetSelection, ""} {
		_, token, err := ledger.Launch("update", "update or rollback")
		if err != nil {
			t.Fatal(err)
		}
		observer := preflighttiming.Open(func(key string) string {
			if key == preflighttiming.DirEnv {
				return channel
			}
			if key == preflighttiming.TokenEnv {
				return token
			}
			return ""
		})
		if scope != "" {
			preflighttiming.Observe(observer, scope, func() (int, error) { return 1, errors.New("original failure") })
		}
		observer.Returned()
		ledger.Finish(token, "fail", 2)
	}
	path := filepath.Join(directory, "live-suite-linux.json")
	if err = os.WriteFile(path, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	operations := filepath.Join(directory, "operations-linux.json")
	if err = os.WriteFile(operations, []byte(`{"schema_version":1,"kind":"live-suite-operations","commit":"`+revision+`","platform":"linux","suite_seconds":10,"operations":[],"cases":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}
	state.PreflightDir = channel
	if _, err = validationduration.Retain(strings.NewReader(""), path, state); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(operations)
	if err != nil {
		t.Fatal(err)
	}
	var evidence validationduration.Operations
	if err = json.Unmarshal(first, &evidence); err != nil {
		t.Fatal(err)
	}
	p, s := evidence.Preflight.Preflight, evidence.Preflight.TargetSelection
	if evidence.Preflight.State != "complete" || evidence.Preflight.Invocations != 3 || p.Measured != 1 || s.Measured != 1 || p.Fail != 1 || s.Fail != 1 || p.NotEntered != 2 || s.NotEntered != 2 || p.Seconds == nil || s.Seconds == nil {
		t.Fatalf("scope totals lost or combined: %s", first)
	}
	if _, err = validationduration.Retain(strings.NewReader(""), path, state); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(operations)
	if err != nil || string(second) != string(first) {
		t.Fatalf("non-idempotent summary %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != record {
		t.Fatal("schema2 changed")
	}
}

// TestPreflightPartialEvidenceRemainsExplicit proves open, lost, unknown,
// unfinished and unattributed calls retain measured P/S independently, and
// strict publication refuses inconsistent totals at the public collector seam.
func TestPreflightPartialEvidenceRemainsExplicit(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	channel, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(channel, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Run string }
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 6; index++ {
		token := fmt.Sprintf("%032x", index)
		common := `"version":1,"run":"` + manifest.Run + `","token":"` + token + `"`
		if index != 6 {
			finish := `,"result":"fail","seconds":10`
			if index == 5 {
				finish = ""
			}
			data := `{` + common + fmt.Sprintf(`,"sequence":%d,"case":"lifecycle","operation":"inspection"`, index) + finish + `}`
			if err := os.WriteFile(filepath.Join(channel, "launch-"+token+".json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if index == 3 {
			continue
		}
		phase := ""
		switch index {
		case 1:
			phase = `,"preflight":{"state":"closed","outcome":"fail","seconds":3},"returned":true`
		case 2:
			phase = `,"preflight":{"state":"open"}`
		case 5:
			phase = `,"preflight":{"state":"closed","outcome":"pass","seconds":2}`
		case 6:
			phase = `,"target_selection":{"state":"closed","outcome":"pass","seconds":5},"returned":true`
		}
		if err := os.WriteFile(filepath.Join(channel, "inv-"+token+".json"), []byte(`{`+common+phase+`}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(channel, "foreign.json"), []byte(`{"secret":"private"}`), 0600); err != nil {
		t.Fatal(err)
	}
	summary := validationduration.CollectPreflight(channel, revision, nil, false)
	reasons := []string{"suite-unfinished", "open-scope", "unknown-dispatch", "unobserved-invocation", "unattributed-record", "invalid-file"}
	if summary.State != "incomplete" || !reflect.DeepEqual(summary.Reasons, reasons) || summary.Invocations != 5 || summary.Unknown != 2 || summary.Unobserved != 1 || summary.Unattributed != 1 || summary.InvalidFiles != 1 || summary.Preflight.Measured != 2 || *summary.Preflight.Seconds != 5 || summary.TargetSelection.Measured != 1 || *summary.TargetSelection.Seconds != 5 {
		t.Fatalf("partial evidence lost: %+v", summary)
	}
	evidence := validationduration.Operations{SchemaVersion: 1, Kind: "live-suite-operations", Commit: revision, Platform: "linux", Preflight: summary}
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err != nil {
		t.Fatal(err)
	}
	summary.Preflight.Measured++
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err == nil {
		t.Fatal("inconsistent counts accepted")
	}
	if data, _ := json.Marshal(summary); strings.Contains(string(data), "secret") || strings.Contains(string(data), manifest.Run) {
		t.Fatal("private journal detail escaped")
	}
}

// TestPreflightCollectionPreservesLegacyAndClassifiesUnavailable proves missing
// or foreign channels never fabricate zero seconds, legacy absence stays absent,
// and sticky ledger loss survives idempotent collection through Retain files.
func TestPreflightCollectionPreservesLegacyAndClassifiesUnavailable(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory := t.TempDir()
	recordPath := filepath.Join(directory, "live-suite-linux.json")
	if err := os.WriteFile(recordPath, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	evidence := validationduration.Operations{SchemaVersion: 1, Kind: "live-suite-operations", Commit: revision, Platform: "linux"}
	path, err := validationduration.WriteOperations(directory, evidence)
	if err != nil {
		t.Fatal(err)
	}
	state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}
	if _, err := validationduration.Retain(strings.NewReader(""), recordPath, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "preflight") || strings.Contains(string(data), "suite_seconds") {
		t.Fatal("legacy values inferred")
	}
	channel, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ path, revision, state, reason string }{
		{filepath.Join(t.TempDir(), "missing"), revision, "not-collected", "channel-unavailable"},
		{channel, strings.Repeat("b", 40), "invalid", "revision-mismatch"},
		{t.TempDir(), revision, "invalid", "manifest-invalid"},
		{channel, revision, "not-collected", "no-invocations"},
	} {
		summary := validationduration.CollectPreflight(scenario.path, scenario.revision, nil, false)
		if summary.State != scenario.state || !slices.Contains(summary.Reasons, scenario.reason) || summary.Preflight.Seconds != nil || summary.TargetSelection.Seconds != nil {
			t.Fatalf("missing != zero: %+v", summary)
		}
	}
	evidence.Preflight = validationduration.CollectPreflight(channel, revision, nil, true)
	if _, err := validationduration.WriteOperations(directory, evidence); err != nil {
		t.Fatal(err)
	}
	state.PreflightDir = channel
	for range 2 {
		if _, err := validationduration.Retain(strings.NewReader(""), recordPath, state); err != nil {
			t.Fatal(err)
		}
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.Preflight.LedgerUnverified || !slices.Contains(evidence.Preflight.Reasons, "invalid-file") {
		t.Fatal("lost launch failure disappeared")
	}
}

// TestPreflightPublicationReconcilesFloatingPointSums proves the public typed
// writer accepts the worked 0.1+0.2=0.3 total within rounding tolerance, while
// rejecting a materially different aggregate and negative suite duration.
func TestPreflightPublicationReconcilesFloatingPointSums(t *testing.T) {
	first, second, sum, inclusive, suite := 0.1, 0.2, 0.3, 1.0, 10.0
	summary := &validationduration.PreflightSummary{State: "complete", Reasons: []string{}, Invocations: 2, Preflight: validationduration.ScopeTotals{Measured: 2, Pass: 2, Seconds: &sum}, TargetSelection: validationduration.ScopeTotals{NotEntered: 2}, Rows: []validationduration.PreflightRow{
		{Sequence: 1, Case: "suite", Operation: "inspection", Result: "pass", Seconds: &inclusive, Preflight: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &first}, TargetSelection: validationduration.ScopeTiming{State: "not-entered"}},
		{Sequence: 2, Case: "suite", Operation: "inspection", Result: "pass", Seconds: &inclusive, Preflight: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &second}, TargetSelection: validationduration.ScopeTiming{State: "not-entered"}},
	}}
	evidence := validationduration.Operations{SchemaVersion: 1, Kind: "live-suite-operations", Commit: "0123456789abcdef0123456789abcdef01234567", Platform: "linux", SuiteSeconds: &suite, Preflight: summary}
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err != nil {
		t.Fatalf("valid rounded sum rejected: %v", err)
	}
	sum = 0.31
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err == nil {
		t.Fatal("incorrect sum accepted")
	}
	sum, suite = 0.3, -1
	if _, err := validationduration.WriteOperations(t.TempDir(), evidence); err == nil {
		t.Fatal("negative suite duration accepted")
	}
}

// TestPreflightUnavailableMarkerRejectsAValidOldJournal proves a supervisor's
// failed-fresh-channel marker prevents reuse of otherwise valid same-revision
// observations through the public collector seam, without inventing zeros.
func TestPreflightUnavailableMarkerRejectsAValidOldJournal(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	run := t.TempDir()
	directory, err := preflighttiming.Create(run, revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(directory, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	_, token, err := ledger.Launch("suite", "inspection")
	if err != nil {
		t.Fatal(err)
	}
	observer := preflighttiming.Open(func(key string) string {
		if key == preflighttiming.DirEnv {
			return directory
		}
		if key == preflighttiming.TokenEnv {
			return token
		}
		return ""
	})
	preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) { return 1, nil })
	observer.Returned()
	ledger.Finish(token, "pass", 1)
	seconds := 10.0
	if before := validationduration.CollectPreflight(directory, revision, &seconds, false); before.State != "complete" {
		t.Fatalf("old channel setup: %+v", before)
	}
	if err := os.WriteFile(filepath.Join(run, "preflight-timing-unverified"), []byte("unavailable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after := validationduration.CollectPreflight(directory, revision, &seconds, false)
	if after.State != "not-collected" || !slices.Contains(after.Reasons, "channel-unavailable") || after.Invocations != 0 || after.Preflight.Seconds != nil || len(after.Rows) != 0 {
		t.Fatalf("unavailable channel reused old observations: %+v", after)
	}
}

// TestPreflightAggregateAboveWindowSurvivesRetain proves individually valid
// intervals may accumulate beyond the suite window, with separate P/S totals,
// complete state and unchanged core/schema2 at WriteOperations and Retain seams.
func TestPreflightAggregateAboveWindowSurvivesRetain(t *testing.T) {
	pEach, sEach, pInclusive, sInclusive := 6000.0, 4000.0, 6200.0, 4500.0
	pTotal, sTotal, suiteSeconds := 12000.0, 8000.0, 7000.0
	evidence := validationduration.Operations{
		SchemaVersion: 1, Kind: "live-suite-operations", Commit: "0123456789abcdef0123456789abcdef01234567", Platform: "linux", CaseOverlap: 4, SuiteSeconds: &suiteSeconds,
		Operations: []validationduration.Operation{
			{Case: "lifecycle", Source: "cli", Operation: "preflight", Result: "pass", Seconds: 6200},
			{Case: "update", Source: "cli", Operation: "preflight", Result: "pass", Seconds: 6200},
			{Case: "agent", Source: "cli", Operation: "inspection", Result: "pass", Seconds: 4500},
			{Case: "t3code", Source: "cli", Operation: "inspection", Result: "pass", Seconds: 4500},
		}, Cases: []validationduration.OperationCase{},
		Preflight: &validationduration.PreflightSummary{State: "complete", Reasons: []string{}, Invocations: 4,
			Preflight:       validationduration.ScopeTotals{Measured: 2, Pass: 2, NotEntered: 2, Seconds: &pTotal},
			TargetSelection: validationduration.ScopeTotals{Measured: 2, Pass: 2, NotEntered: 2, Seconds: &sTotal},
			Rows: []validationduration.PreflightRow{
				{Sequence: 1, Case: "lifecycle", Operation: "preflight", Result: "pass", Seconds: &pInclusive, Preflight: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &pEach}, TargetSelection: validationduration.ScopeTiming{State: "not-entered"}},
				{Sequence: 2, Case: "update", Operation: "preflight", Result: "pass", Seconds: &pInclusive, Preflight: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &pEach}, TargetSelection: validationduration.ScopeTiming{State: "not-entered"}},
				{Sequence: 3, Case: "agent", Operation: "inspection", Result: "pass", Seconds: &sInclusive, Preflight: validationduration.ScopeTiming{State: "not-entered"}, TargetSelection: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &sEach}},
				{Sequence: 4, Case: "t3code", Operation: "inspection", Result: "pass", Seconds: &sInclusive, Preflight: validationduration.ScopeTiming{State: "not-entered"}, TargetSelection: validationduration.ScopeTiming{State: "closed", Outcome: "pass", Seconds: &sEach}},
			},
		},
	}
	directory := t.TempDir()
	operations, err := validationduration.WriteOperations(directory, evidence)
	if err != nil {
		t.Fatalf("valid accumulated totals rejected: %v", err)
	}
	recordPath := filepath.Join(directory, "live-suite-linux.json")
	if err := os.WriteFile(recordPath, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validationduration.Retain(strings.NewReader(""), recordPath, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(operations)
	if err != nil {
		t.Fatal(err)
	}
	var got validationduration.Operations
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Preflight == nil || got.Preflight.State != "complete" || len(got.Preflight.Reasons) != 0 || got.Preflight.Preflight.Seconds == nil || *got.Preflight.Preflight.Seconds != 12000 || got.Preflight.TargetSelection.Seconds == nil || *got.Preflight.TargetSelection.Seconds != 8000 || !reflect.DeepEqual(got, evidence) {
		t.Fatalf("accumulated scopes or independent evidence changed: %s", data)
	}
	if data, err := os.ReadFile(recordPath); err != nil || string(data) != record {
		t.Fatal("schema2 changed")
	}
}
