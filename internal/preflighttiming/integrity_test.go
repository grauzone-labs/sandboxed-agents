package preflighttiming_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// observedChannel creates an isolated channel and ledger for public API tests.
func observedChannel(t *testing.T) (string, *preflighttiming.Ledger) {
	t.Helper()
	dir, err := preflighttiming.Create(t.TempDir(), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	return dir, ledger
}

// observerFor opens one validated process observation using only the issued token.
func observerFor(dir, token string) *preflighttiming.Observer {
	return preflighttiming.Open(func(key string) string {
		if key == preflighttiming.DirEnv {
			return dir
		}
		if key == preflighttiming.TokenEnv {
			return token
		}
		return ""
	})
}

// TestObservationRetainsOpenThenClosed proves a blocked call is durably entered
// before it returns and retains the same value/error after closing at the journal seam.
func TestObservationRetainsOpenThenClosed(t *testing.T) {
	dir, ledger := observedChannel(t)
	_, token, err := ledger.Launch("update", "update or rollback")
	if err != nil {
		t.Fatal(err)
	}
	observer := observerFor(dir, token)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	failure := errors.New("private failure")
	go func() {
		value, err := preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) { close(entered); <-release; return 73, failure })
		observer.Returned()
		if value != 73 || err != failure {
			done <- errors.New("pass-through changed")
		} else {
			done <- nil
		}
	}()
	<-entered
	journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	phase := journal.Entries[0].Invocation.Preflight
	if phase == nil || phase.State != "open" || phase.Seconds != nil {
		t.Fatalf("blocked phase %+v", phase)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	journal, err = preflighttiming.Read(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	phase = journal.Entries[0].Invocation.Preflight
	if phase.State != "closed" || phase.Outcome != "fail" || phase.Seconds == nil {
		t.Fatalf("returned phase %+v", phase)
	}
}

// TestLedgerConcurrentLaunchesAndIOLoss proves unique ordered attribution under
// concurrency and sticky write-loss detection at the private file boundary.
func TestLedgerConcurrentLaunchesAndIOLoss(t *testing.T) {
	dir, ledger := observedChannel(t)
	var calls sync.WaitGroup
	for range 32 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, token, err := ledger.Launch("agent", "agent session")
			if err != nil {
				t.Error(err)
				return
			}
			ledger.Finish(token, "fail", 1.25)
		}()
	}
	calls.Wait()
	journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Entries) != 32 || journal.InvalidFiles != 0 {
		t.Fatalf("journal %+v", journal)
	}
	for index, entry := range journal.Entries {
		if entry.Launch.Sequence != index+1 || entry.Launch.Seconds == nil || *entry.Launch.Seconds != 1.25 {
			t.Fatalf("launch %+v", entry.Launch)
		}
	}
	_, token, err := ledger.Launch("agent", "agent session")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "launch-"+token+".json")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ledger.Finish(token, "pass", 2)
	if !ledger.Unverified() {
		t.Fatal("lost final attribution could be called complete")
	}
}

// TestJournalRejectsForeignAndUnsafeFiles proves bounded strict reads exclude
// stale runs, links, payload fields, repeat observations and oversized records.
func TestJournalRejectsForeignAndUnsafeFiles(t *testing.T) {
	dir, ledger := observedChannel(t)
	_, token, err := ledger.Launch("preflight", "preflight")
	if err != nil {
		t.Fatal(err)
	}
	observer := observerFor(dir, token)
	preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) { return 1, nil })
	observer.Returned()
	ledger.Finish(token, "pass", 1)
	data, err := os.ReadFile(filepath.Join(dir, "inv-"+token+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err = json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	for index, kind := range []string{"foreign", "payload", "repeated", "oversized"} {
		next := strings.Repeat(string(rune('d'+index)), 32)
		copy := map[string]any{}
		for key, value := range base {
			copy[key] = value
		}
		copy["token"] = next
		switch kind {
		case "foreign":
			copy["run"] = strings.Repeat("f", 64)
		case "payload":
			copy["output"] = "secret host path"
		case "repeated":
			copy["preflight"] = map[string]string{"state": "repeated"}
		}
		encoded, _ := json.Marshal(copy)
		if kind == "oversized" {
			encoded = []byte(strings.Repeat("x", 4097))
		}
		if err = os.WriteFile(filepath.Join(dir, "inv-"+next+".json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(dir, ".tmp-ignored"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	expected := 4
	if err = os.Symlink(filepath.Join(dir, "inv-"+token+".json"), filepath.Join(dir, "inv-"+strings.Repeat("1", 32)+".json")); err == nil {
		expected++
	}
	journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if journal.InvalidFiles != expected || len(journal.Entries) != 1 {
		t.Fatalf("unsafe files accepted: %+v", journal)
	}
}

// TestObserverIOLossDoesNotChangeCalls proves unavailable or unwritable journals
// cannot change the original returned value/error at the pass-through seam.
func TestObserverIOLossDoesNotChangeCalls(t *testing.T) {
	dir, ledger := observedChannel(t)
	_, token, err := ledger.Launch("update", "update or rollback")
	if err != nil {
		t.Fatal(err)
	}
	observer := observerFor(dir, token)
	path := filepath.Join(dir, "inv-"+token+".json")
	_ = os.Remove(path)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("original error")
	for _, o := range []*preflighttiming.Observer{observer, nil, observerFor("relative", token), observerFor(dir, "../invalid")} {
		value, err := preflighttiming.Observe(o, preflighttiming.TargetSelection, func() (int, error) { return 12, failure })
		if value != 12 || err != failure {
			t.Fatal("supplemental I/O changed a call")
		}
		o.Returned()
	}
	journal, err := preflighttiming.Read(dir, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if journal.InvalidFiles != 1 {
		t.Fatalf("lost observation accepted: %+v", journal)
	}
}

// TestJournalBoundsAndManifestIdentity proves directory overflow, invalid
// manifest identity and symlinked roots cannot produce complete measurements
// through the bounded public reader and observer APIs.
func TestJournalBoundsAndManifestIdentity(t *testing.T) {
	directory, ledger := observedChannel(t)
	// Empty ignored temporary files still count toward the directory read bound.
	for index := 0; index < preflighttiming.EntryLimit; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf(".tmp-%d", index)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := preflighttiming.Read(directory, strings.Repeat("a", 40))
	if err != nil || journal.InvalidFiles != 1 || len(journal.Entries) != 0 {
		t.Fatalf("unbounded directory accepted: %+v %v", journal, err)
	}
	_, _, _ = ledger.Launch("lifecycle", "inspection")
	if !ledger.Unverified() {
		t.Fatal("directory overflow was not sticky")
	}
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "manifest.json"), []byte(`{"version":1,"kind":"live-preflight-timing","run":"`+strings.Repeat("a", 64)+`","revision":"`+strings.Repeat("a", 40)+`"} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := preflighttiming.Read(bad, strings.Repeat("a", 40)); !errors.Is(err, preflighttiming.ErrManifest) {
		t.Fatalf("trailing manifest accepted: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(bad, link) == nil {
		if _, err := preflighttiming.Read(link, strings.Repeat("a", 40)); !errors.Is(err, preflighttiming.ErrUnavailable) {
			t.Fatalf("linked root accepted: %v", err)
		}
	}
}
