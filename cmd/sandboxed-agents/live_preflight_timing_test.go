package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveTimingTokensCorrelateActualChildren proves concurrent built-source CLI
// children receive distinct tokens, exact existing operation durations and no
// ambient token reuse; a cancelled remove queue creates no launch at the host seam.
func TestLiveTimingTokensCorrelateActualChildren(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(directory, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	environment := testutil.Env(fakes.Env(os.Environ()), map[string]string{preflighttiming.DirEnv: directory, preflighttiming.TokenEnv: "ambient-token-must-not-be-used"})
	original := append([]string(nil), environment...)
	host := liveHost{binary: executable, environment: environment, ledger: ledger, timings: &liveTimings{}, caseName: "lifecycle", removals: make(chan struct{}, 1)}
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			if _, err := host.cli("", "version"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	host.removals <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = host.cliContext(ctx, "", "remove", "queued", "--volumes")
	<-host.removals
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queue cancellation: %v", err)
	}
	journal, err := preflighttiming.Read(directory, revision)
	if err != nil || len(journal.Entries) != 2 {
		t.Fatalf("actual child launches = %d, error %v; want 2", len(journal.Entries), err)
	}
	for _, entry := range journal.Entries {
		if entry.Launch == nil || entry.Invocation == nil || !entry.Invocation.Returned || entry.Invocation.Preflight != nil || entry.Invocation.TargetSelection != nil || entry.Launch.Result != "pass" || entry.Launch.Seconds == nil {
			t.Fatalf("uncorrelated version: %+v", entry)
		}
		matches := 0
		for _, operation := range host.timings.evidence.Operations {
			if operation.Operation == "preflight" && operation.Seconds == *entry.Launch.Seconds {
				matches++
			}
		}
		if matches != 1 {
			t.Fatal("ledger did not retain the exact existing duration")
		}
	}
	if !reflect.DeepEqual(original, host.environment) {
		t.Fatal("shared host environment mutated")
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveTimingTerminalCorrelatesOnce proves a built-source terminal child's
// timing is finalized once across Wait and Close at the terminal host seam.
func TestLiveTimingTerminalCorrelatesOnce(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(directory, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	host := liveHost{binary: executable, environment: testutil.Env(fakes.Env(os.Environ()), map[string]string{preflighttiming.DirEnv: directory}), ledger: ledger, timings: &liveTimings{}, caseName: "agent"}
	terminal := host.startTerminal(t, "version")
	defer terminal.Close()
	if exit, done := terminal.Wait(30 * time.Second); !done || exit != 0 {
		t.Fatalf("version terminal: exit %d, done %v", exit, done)
	}
	terminal.Close()
	journal, err := preflighttiming.Read(directory, revision)
	if err != nil || len(journal.Entries) != 1 || len(host.timings.evidence.Operations) != 1 {
		t.Fatalf("terminal correlation: %+v, %v", journal, err)
	}
	entry := journal.Entries[0]
	if entry.Launch == nil || entry.Invocation == nil || entry.Launch.Seconds == nil || *entry.Launch.Seconds != host.timings.evidence.Operations[0].Seconds || entry.Launch.Result != "pass" {
		t.Fatal("terminal duration was missing or recomputed")
	}
	assertNoExternalCalls(t, fakes)
}

// TestLiveTimingSuiteBoundaryAndProvenance proves the suite's initial artifact
// omits T, final cleanup retains T, and executable overrides disable tokens at
// the actual newLiveSuite/operations file seam without running an engine.
func TestLiveTimingSuiteBoundaryAndProvenance(t *testing.T) {
	for _, override := range []string{"", "TEST_SANDBOXED_AGENTS_EXECUTABLE", livePreparedExecutableEnv} {
		t.Run("override="+override, func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			t.Setenv("TEST_SANDBOXED_AGENTS_EXECUTABLE", "")
			t.Setenv(livePreparedExecutableEnv, "")
			if override != "" {
				t.Setenv(override, "unverified-fixture")
			}
			directory := t.TempDir()
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			revision, err := liveCommit(root)
			if err != nil {
				t.Fatal(err)
			}
			channel, err := preflighttiming.Create(t.TempDir(), revision)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(preflighttiming.DirEnv, channel)
			t.Setenv(liveRecordDirEnv, directory)
			t.Setenv("SANDBOXED_AGENTS_GROUP", "offline-timing")
			started := time.Now()
			var cleanupElapsed float64
			t.Run("suite", func(t *testing.T) {
				suite := newLiveSuite(t)
				initial := readTimingOperations(t, directory)
				if initial.SuiteSeconds != nil || initial.Preflight != nil {
					t.Fatal("initial evidence fabricated completion")
				}
				lower := time.Now()
				t.Cleanup(func() { cleanupElapsed = time.Since(lower).Seconds() })
				if _, err := suite.host.cli("", "version"); err != nil {
					t.Fatal(err)
				}
			})
			evidence := readTimingOperations(t, directory)
			if evidence.SuiteSeconds == nil || *evidence.SuiteSeconds < cleanupElapsed || *evidence.SuiteSeconds > time.Since(started).Seconds() {
				t.Fatalf("invalid suite boundary: %+v", evidence.SuiteSeconds)
			}
			summary := validationduration.CollectPreflight(channel, revision, evidence.SuiteSeconds, false)
			want := 1
			if override != "" {
				want = 0
			}
			if summary.Invocations != want {
				t.Fatalf("override %q: launches %d want %d", override, summary.Invocations, want)
			}
			assertNoExternalCalls(t, fakes)
		})
	}
}

// readTimingOperations decodes the actual live harness artifact after a lifecycle boundary.
func readTimingOperations(t *testing.T, directory string) validationduration.Operations {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "operations-"+livePlatform()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence validationduration.Operations
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence
}

// TestLiveTimingInterruptedProcessRetainsOpenScope proves cancelling an actual
// built CLI while fake Podman blocks preserves the entered scope, failed
// inclusive operation, and incomplete collector summary at the full host seam.
func TestLiveTimingInterruptedProcessRetainsOpenScope(t *testing.T) {
	var baselinePodman, baselineSSH [][]string
	var baselineOperations []validationduration.Operation
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			fakes := testutil.Install(t, fakeExecutable)
			t.Setenv("PATH", fakes.Dir)
			t.Setenv("TEST_SANDBOXED_AGENTS_EXECUTABLE", "")
			t.Setenv(livePreparedExecutableEnv, "")
			root, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			revision, err := liveCommit(root)
			if err != nil {
				t.Fatal(err)
			}
			channel, err := preflighttiming.Create(t.TempDir(), revision)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(preflighttiming.DirEnv, "")
			if observed {
				t.Setenv(preflighttiming.DirEnv, channel)
			}
			directory := t.TempDir()
			t.Setenv(liveRecordDirEnv, directory)
			t.Setenv("SANDBOXED_AGENTS_GROUP", "offline-timing")
			ready, release := filepath.Join(directory, "query-ready"), filepath.Join(directory, "query-release")
			script := `{"--version":{"ReadyFile":` + quoteLiveJSON(ready) + `,"WaitFor":` + quoteLiveJSON(release) + `}}`
			t.Run("suite", func(t *testing.T) {
				suite := newLiveSuite(t)
				suite.host.environment = testutil.Env(fakes.Env(suite.host.environment), map[string]string{"SANDBOXED_AGENTS_GROUP": "offline-timing", "TEST_PODMAN_RESPONSES": script})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := suite.host.cliContext(ctx, "", "check"); done <- err }()
				waitLiveMarker(t, ready)
				if observed {
					journal, err := preflighttiming.Read(channel, revision)
					if err != nil || len(journal.Entries) != 1 || journal.Entries[0].Invocation == nil || journal.Entries[0].Invocation.Preflight == nil || journal.Entries[0].Invocation.Preflight.State != "open" {
						t.Fatalf("preflight not open at blocked query: %+v %v", journal, err)
					}
				}
				cancel()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancelled CLI passed")
					}
				case <-time.After(30 * time.Second):
					t.Fatal("cancelled CLI did not end")
				}
			})
			evidence := readTimingOperations(t, directory)
			if evidence.SuiteSeconds == nil || len(evidence.Operations) < 1 || evidence.Operations[0].Source != "cli" || evidence.Operations[0].Result != "fail" {
				t.Fatal("harness failed operation or suite boundary lost")
			}
			calls, ssh := testutil.Invocations(t, fakes.PodmanLog), testutil.Invocations(t, fakes.SSHLog)
			if !observed {
				baselinePodman, baselineSSH = calls, ssh
				baselineOperations = evidence.Operations
				return
			}
			if !reflect.DeepEqual(calls, baselinePodman) || !reflect.DeepEqual(ssh, baselineSSH) {
				t.Fatalf("timing added query delta != 0: podman %v/%v ssh %v/%v", calls, baselinePodman, ssh, baselineSSH)
			}
			// The baseline includes the existing #181 post-failure diagnostic read.
			if len(baselinePodman) < 2 || len(evidence.Operations) != len(baselineOperations) || len(evidence.Operations) < 2 || evidence.Operations[1].Source != "podman" {
				t.Fatal("existing diagnostic query or its timing was lost")
			}
			recordPath := filepath.Join(directory, "live-suite-"+livePlatform()+".json")
			before, err := os.ReadFile(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validationduration.Retain(strings.NewReader(""), recordPath, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", PreflightDir: channel})
			if err != nil {
				t.Fatal(err)
			}
			evidence = readTimingOperations(t, directory)
			if evidence.Preflight == nil || evidence.Preflight.State != "incomplete" || evidence.Preflight.Preflight.Open != 1 || evidence.Preflight.Preflight.Seconds != nil || !slices.Contains(evidence.Preflight.Reasons, "open-scope") {
				t.Fatalf("interruption misclassified: %+v", evidence.Preflight)
			}
			after, err := os.ReadFile(recordPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("supplemental collection changed suite verdict")
			}
		})
	}
}

// TestLiveTimingRefusesStaleLedger proves a suite refusing an already used
// channel cannot later publish its old same-revision observations, through the
// newLiveSuite final-write and Retain file seams with an actual built version call.
func TestLiveTimingRefusesStaleLedger(t *testing.T) {
	fakes := testutil.Install(t, fakeExecutable)
	t.Setenv("PATH", fakes.Dir)
	t.Setenv("TEST_SANDBOXED_AGENTS_EXECUTABLE", "")
	t.Setenv(livePreparedExecutableEnv, "")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := liveCommit(root)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := preflighttiming.Create(t.TempDir(), revision)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := preflighttiming.OpenLedger(channel, revision)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := ledger.Launch("suite", "preflight")
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
	preflighttiming.Observe(observer, preflighttiming.Preflight, func() (int, error) { return 1, nil })
	observer.Returned()
	ledger.Finish(token, "pass", 1)
	ledger.Close()
	directory := t.TempDir()
	t.Setenv(preflighttiming.DirEnv, channel)
	t.Setenv(liveRecordDirEnv, directory)
	t.Setenv("SANDBOXED_AGENTS_GROUP", "offline-timing")
	t.Run("suite", func(t *testing.T) {
		suite := newLiveSuite(t)
		if suite.host.ledger != nil {
			t.Fatal("stale ledger admitted")
		}
		if _, err := suite.host.cli("", "version"); err != nil {
			t.Fatal(err)
		}
	})
	evidence := readTimingOperations(t, directory)
	if evidence.Preflight == nil || evidence.Preflight.State != "not-collected" || !slices.Contains(evidence.Preflight.Reasons, "channel-unavailable") {
		t.Fatalf("refused channel not retained: %+v", evidence.Preflight)
	}
	if _, err := validationduration.Retain(strings.NewReader(""), filepath.Join(directory, "live-suite-"+livePlatform()+".json"), validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", PreflightDir: channel}); err != nil {
		t.Fatal(err)
	}
	evidence = readTimingOperations(t, directory)
	if evidence.Preflight.State != "not-collected" || evidence.Preflight.Invocations != 0 || evidence.Preflight.Preflight.Seconds != nil {
		t.Fatal("collector reactivated a refused channel")
	}
	assertNoExternalCalls(t, fakes)
}
