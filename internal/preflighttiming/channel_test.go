package preflighttiming_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// TestJournalSeparatesReturnedScopes proves one launched preflight and a failed
// target-only call retain their distinct values and outcomes at the journal seam.
func TestJournalSeparatesReturnedScopes(t *testing.T) {
	revision := strings.Repeat("a", 40)
	dir, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(dir, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, scope := range []preflighttiming.Scope{preflighttiming.Preflight, preflighttiming.TargetSelection} {
		_, token, err := ledger.Launch("update", "update or rollback")
		if err != nil {
			t.Fatal(err)
		}
		observer := preflighttiming.Open(func(key string) string {
			switch key {
			case preflighttiming.DirEnv:
				return dir
			case preflighttiming.TokenEnv:
				return token
			}
			return ""
		})
		failure := errors.New("private failure payload")
		value, outcome := preflighttiming.Observe(observer, scope, func() (string, error) {
			if scope == preflighttiming.TargetSelection {
				return "same value", failure
			}
			return "same value", nil
		})
		if value != "same value" || scope == preflighttiming.TargetSelection && outcome != failure {
			t.Fatal("observer changed returned value/error")
		}
		observer.Returned()
		ledger.Finish(token, "pass", 1)
	}
	journal, err := preflighttiming.Read(dir, revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Entries) != 2 || journal.InvalidFiles != 0 {
		t.Fatalf("journal %+v", journal)
	}
	first, second := journal.Entries[0].Invocation, journal.Entries[1].Invocation
	if first.Preflight == nil || first.Preflight.Outcome != "pass" || first.TargetSelection != nil || second.TargetSelection == nil || second.TargetSelection.Outcome != "fail" || second.Preflight != nil || !first.Returned || !second.Returned {
		t.Fatalf("scopes %+v %+v", first, second)
	}
	if _, err = preflighttiming.Create(strings.TrimSuffix(dir, "/preflight-timing"), revision); err == nil {
		t.Fatal("stale run directory reused")
	}
	if _, err = preflighttiming.OpenLedger(dir, revision); err == nil {
		t.Fatal("nonempty ledger reused")
	}
	if _, err = preflighttiming.Read(dir, strings.Repeat("b", 40)); !errors.Is(err, preflighttiming.ErrRevision) {
		t.Fatalf("revision error %v", err)
	}
}
