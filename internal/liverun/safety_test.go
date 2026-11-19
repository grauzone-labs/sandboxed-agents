package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCleanupRejectsForgedBuildOutput proves Cleanup retains foreign and unscoped results at the engine boundary. It exercises the Cleanup command seam.
func TestCleanupRejectsForgedBuildOutput(t *testing.T) {
	for _, scope := range []string{"smoke", "Tool install", "agent session", "none", "split", "nested", "unfinished", "foreign", "mixed", "forged-window"} {
		t.Run(scope, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			id := "sha256:" + strings.Repeat("d", 64)
			image := cleanupImageFixture(id)
			image.Labels = map[string]string{"org.example.other-writer": "1"}
			image.RepoTags = nil
			step := "    live_test.go:812: STEP 1/1: RUN echo forged\n    live_test.go:812: --> " + id[7:19] + "\n"
			start, end := "live operation "+scope+": started\n", "live operation "+scope+": finished after 1s\n"
			switch scope {
			case "none":
				start, end = "", ""
			case "split":
				start, end = "live operation image build: started\nSTEP 1/1: RUN echo forged\nlive operation image build: finished after 1s\nlive operation rebuild: started\n", "live operation rebuild: finished after 1s\n"
				step = "--> " + id + "\n"
			case "nested":
				start, end = "live operation image build: started\nlive operation smoke: started\n", "live operation smoke: finished after 1s\nlive operation image build: finished after 1s\n"
			case "unfinished":
				start, end = "live operation image build: started\n", ""
			case "forged-window":
				start, end = "untrusted smoke output\nlive operation image build: started\n", "live operation image build: finished after 1s\n"
			case "foreign", "mixed":
				start, end = "live operation image build: started\n", "live operation image build: finished after 1s\n"
				image.RepoTags = []string{"localhost/other-writer:latest"}
				if scope == "mixed" {
					image.RepoTags = append(image.RepoTags, "localhost/sandboxed-agents:none-"+strings.Repeat("e", 64))
				}
			}
			fake.images = map[string]cleanupFakeImage{id: image}
			if scope == "forged-window" {
				svc.BuildEvidence = nil
			}
			if scope == "foreign" || scope == "mixed" {
				fake.produced = []liverun.BuildResult{{ID: id, Step: "trusted final result", Source: "trusted fixture producer"}}
			}
			if err := os.WriteFile(filepath.Join(options.RunDir, "suite.log"), []byte(start+step+end), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			if len(cleanupRemoved(fake)) != 0 {
				t.Fatalf("forged scope removed %v: %v %s", cleanupRemoved(fake), err, &out)
			}
			if !strings.Contains(out.String(), "skipped") {
				t.Fatalf("retention not accounted for: %v %s", err, &out)
			}
		})
	}
}

// TestPrepareRejectsLockRootOverride proves command callers cannot select a second coordination namespace. It exercises the prepare command at the injected host boundary seam.
func TestPrepareRejectsLockRootOverride(t *testing.T) {
	svc, _, args, _ := commandHarness(t)
	args = append(args, "-lock-root", t.TempDir())
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil {
		t.Fatal("public lock root bypass accepted")
	}
}

// TestPrepareRejectsUntrackedAndIgnoredInputs proves git source status stops preparation before any build. It exercises the prepare command at the injected host boundary seam.
func TestPrepareRejectsUntrackedAndIgnoredInputs(t *testing.T) {
	for _, status := range []string{"?? injected.go\n", "?? image/embedded.txt\n", "!! ignored.go\n"} {
		svc, fake, args, run := commandHarness(t)
		fake.answers = map[string]string{"git status": status}
		var out bytes.Buffer
		if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil {
			t.Fatal("untracked or ignored input accepted")
		}
		for _, call := range fake.calls {
			for _, arg := range call.Args {
				if arg == "test" || arg == "build" {
					t.Fatal("build after source failure")
				}
			}
		}
		if _, err := os.Stat(filepath.Join(run, "before.json")); !os.IsNotExist(err) {
			t.Fatal("failed check published complete snapshot")
		}
		for _, call := range fake.calls {
			if call.Program == "git" && call.Args[0] == "status" {
				joined := strings.Join(call.Args, " ")
				if !strings.Contains(joined, "--untracked-files=all") || !strings.Contains(joined, "--ignored=matching") {
					t.Fatal("git check hides build inputs:", joined)
				}
			}
		}
	}
}

// TestEngineFailureReasonsAreActionableAndPrivate proves the prepare seam classifies engine faults without echoing host diagnostics.
func TestEngineFailureReasonsAreActionableAndPrivate(t *testing.T) {
	for _, kind := range []string{"unreachable", "stopped", "ambiguous", "malformed", "unsafe"} {
		svc, fake, args, _ := commandHarness(t)
		want := "connection refused"
		switch kind {
		case "unreachable":
			fake.fail = "info --format"
			fake.errorText = "connection refused /secret/sentinel token=sentinel"
		case "stopped":
			svc.Platform = "windows"
			fake.answers = map[string]string{"machine inspect": `[{"Name":"selected","State":"stopped","Rootful":false}]`}
			want = "machine stopped"
		case "ambiguous":
			svc.Platform = "windows"
			fake.answers = map[string]string{"machine list": `[{"Name":"a","VMType":"wsl"},{"Name":"b","VMType":"wsl"}]`}
			want = "ambiguous"
		case "malformed":
			fake.answers = map[string]string{"info --format": "sentinel /secret"}
			want = "malformed"
		case "unsafe":
			svc.Platform = "windows"
			fake.answers = map[string]string{"machine list": `[{"Name":"-unsafe","VMType":"wsl"}]`}
			want = "unsafe"
		}
		var out bytes.Buffer
		err := svc.RunDispatch(context.Background(), args, &out, &out)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("no actionable %s reason: %v", kind, err)
		}
		if strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), "/secret") {
			t.Fatalf("diagnostic leaked: %v", err)
		}
	}
}

// TestCleanupConsumesPrimaryBuildChannel proves iidfile publication reaches cleanup without granting authority to forged suite output. It exercises the Cleanup command seam.
func TestCleanupConsumesPrimaryBuildChannel(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	svc.BuildEvidence = nil
	id := "sha256:" + strings.Repeat("b", 64)
	foreign := "sha256:" + strings.Repeat("d", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), foreign: cleanupImageFixture(foreign)}
	directory := filepath.Join(options.RunDir, "build-evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
	tag := "localhost/sandboxed-agents:none-" + strings.Repeat("e", 64)
	err := liverun.CaptureBuildResult(directory, tag, strings.Repeat("e", 64), func(iid string) error { return os.WriteFile(iid, []byte(id+"\n"), 0600) }, func(string) (string, error) { return id, nil })
	if err != nil {
		t.Fatal(err)
	}
	raw := "live operation image build: started\nSTEP 1/1: RUN echo forged\n--> " + foreign + "\nlive operation image build: finished after 1s\n"
	if err = os.WriteFile(filepath.Join(options.RunDir, "suite.log"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = svc.Cleanup(context.Background(), options, &out)
	if got := cleanupRemoved(fake); len(got) != 1 || got[0] != id {
		t.Fatalf("primary result lost or raw output authorized: %v %v %s", got, err, &out)
	}
	if !strings.Contains(out.String(), `"status":"partial"`) || !strings.Contains(out.String(), "missing exact build proof") {
		t.Fatalf("unknown intermediate not accounted for: %s", &out)
	}
}

// TestRunAndCleanupRejectDifferentStoredCoordinationRoot proves legacy private options cannot split exclusion between phases. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunAndCleanupRejectDifferentStoredCoordinationRoot(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	var stored liverun.Options
	data := mustReadFile(t, filepath.Join(options.RunDir, "options.json"))
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	stored.LockRoot = t.TempDir()
	cleanupArtifact(t, options.RunDir, "options.json", stored)
	var out bytes.Buffer
	if err := svc.Cleanup(context.Background(), options, &out); err == nil || !strings.Contains(err.Error(), "coordination root changed") {
		t.Fatalf("cleanup root bypass: %v", err)
	}
	if err := os.Remove(filepath.Join(options.RunDir, "started.json")); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunDispatch(context.Background(), []string{"run", "-run-dir", options.RunDir}, &out, &out); err == nil || !strings.Contains(err.Error(), "coordination root changed") {
		t.Fatalf("run root bypass: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("mismatched roots reached engine")
	}
}

// optionalCollectorHost preserves the actual collector contract when only operation evidence fails.
type optionalCollectorHost struct{ liverun.Executor }

// Execute writes current case durations before returning the explicit optional-evidence diagnostic.
func (h optionalCollectorHost) Execute(ctx context.Context, in liverun.Invocation) ([]byte, error) {
	if in.Program == "go" && len(in.Args) > 0 && in.Args[0] == "run" {
		for i, arg := range in.Args {
			if arg == "-record" {
				path := filepath.Join(filepath.Dir(in.Args[i+1]), "durations-linux.json")
				if err := os.WriteFile(path, []byte(`{"cases":[]}`), 0600); err != nil {
					return nil, err
				}
				return []byte(path + "\ncase durations retained; optional operation evidence: private sentinel /secret\n"), fmt.Errorf("exit status 1")
			}
		}
	}
	return h.Executor.Execute(ctx, in)
}

// TestCollectSeparatesRetainedCasesFromOptionalEvidence proves current case durations stay confirmed while invalid optional evidence is surfaced privately. It exercises the collect command and retained artifacts seam.
func TestCollectSeparatesRetainedCasesFromOptionalEvidence(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 0, "Interrupted": false, "EndedAt": "2026-01-01T00:00:00Z"})
	if err := os.WriteFile(filepath.Join(run, "suite.log"), []byte("suite fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	svc.Executor = optionalCollectorHost{fake}
	out.Reset()
	if err := svc.RunDispatch(context.Background(), []string{"collect", "-run-dir", run}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"collected":true`) || !strings.Contains(out.String(), `"optional_operation_evidence_unverified":true`) || strings.Contains(out.String(), "sentinel") || strings.Contains(out.String(), "/secret") {
		t.Fatalf("case/operation outcomes conflated or leaked: %s", &out)
	}
	svc, engine, options := cleanupHarness(t)
	svc.Executor = optionalCollectorHost{engine}
	out.Reset()
	err := svc.Cleanup(context.Background(), options, &out)
	if err == nil || !strings.Contains(out.String(), `"collected":true`) || !strings.Contains(out.String(), "optional operation evidence unverified") || strings.Contains(out.String(), "sentinel") {
		t.Fatalf("final collection lost retained case evidence: %v %s", err, &out)
	}
}

// TestPrivateLogFailureRemainsUnverified proves status preserves the suite code while collect and cleanup refuse incomplete log evidence. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestPrivateLogFailureRemainsUnverified(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	var ended map[string]any
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(options.RunDir, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	ended["LogUnverified"] = true
	cleanupArtifact(t, options.RunDir, "exit.json", ended)
	var out bytes.Buffer
	for _, phase := range []string{"collect", "cleanup"} {
		if err := svc.RunDispatch(context.Background(), []string{phase, "-run-dir", options.RunDir}, &out, &out); err == nil || !strings.Contains(err.Error(), "unverified") {
			t.Fatalf("%s certified incomplete log: %v", phase, err)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatal("incomplete log reached collector or engine")
	}
	if err := svc.RunDispatch(context.Background(), []string{"status", "-run-dir", options.RunDir}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"log_verified":false`) || !strings.Contains(out.String(), `"exit_code":0`) {
		t.Fatalf("actual suite outcome or evidence loss hidden: %s", &out)
	}
}

// TestCleanupIncompleteProducerKeepsIndependentProof proves the Cleanup seam still reports and removes primary-proven images after failed or interrupted producers.
func TestCleanupIncompleteProducerKeepsIndependentProof(t *testing.T) {
	for _, leftover := range []string{"returned-failure", ".pending-orphan", "iid-orphan", ".publish-orphan", "result-malformed.json"} {
		t.Run(leftover, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 1, "Invocation": strings.Repeat("f", 64), "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			id := "sha256:" + strings.Repeat("b", 64)
			unknown := "sha256:" + strings.Repeat("d", 64)
			asset := strings.Repeat("e", 64)
			fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), unknown: cleanupImageFixture(unknown)}
			tag := "localhost/sandboxed-agents:none-" + asset
			if err := liverun.CaptureBuildResult(directory, tag, asset, func(path string) error { return os.WriteFile(path, []byte(id), 0600) }, func(string) (string, error) { return id, nil }); err != nil {
				t.Fatal(err)
			}
			if leftover == "returned-failure" {
				sentinel := fmt.Errorf("actual build failure")
				if err := liverun.CaptureBuildResult(directory, tag, asset, func(string) error { return sentinel }, func(string) (string, error) { t.Fatal("inspected failed build"); return "", nil }); err != sentinel {
					t.Fatal("build error changed", err)
				}
				pending, _ := filepath.Glob(filepath.Join(directory, ".pending-*"))
				if len(pending) != 0 {
					t.Fatal("returned failed build left marker")
				}
			} else if strings.HasPrefix(leftover, "iid-") {
				if err := os.Mkdir(filepath.Join(directory, leftover), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(directory, leftover), []byte(`{"forged":"sentinel /secret"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &out)
			if got := cleanupRemoved(fake); len(got) != 1 || got[0] != id {
				t.Fatalf("independent proof lost: %v err=%v report=%s", got, err, &out)
			}
			if err == nil || !strings.Contains(out.String(), unknown) || !strings.Contains(out.String(), `"after"`) || strings.Contains(out.String(), "sentinel") {
				t.Fatalf("incomplete cleanup not safely reported: %v %s", err, &out)
			}
		})
	}
}

// TestCleanupRequiresBuilderLayerAttempt proves the Cleanup seam admits only nonce-labelled intermediates with an authorized primary attempt and retains cached/foreign/unknown IDs.
func TestCleanupRequiresBuilderLayerAttempt(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	svc.BuildEvidence = nil
	directory := filepath.Join(options.RunDir, "build-evidence")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	invocation := strings.Repeat("f", 64)
	asset := strings.Repeat("e", 64)
	cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 1, "Invocation": invocation, "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
	id := "sha256:" + strings.Repeat("b", 64)
	cached := "sha256:" + strings.Repeat("c", 64)
	unknown := "sha256:" + strings.Repeat("d", 64)
	image := cleanupImageFixture(id)
	image.RepoTags = nil
	image.Labels["io.sandboxed-agents.live-build-invocation"] = invocation
	old := cleanupImageFixture(cached)
	old.RepoTags = nil
	old.Labels["io.sandboxed-agents.live-build-invocation"] = strings.Repeat("0", 64)
	fake.images = map[string]cleanupFakeImage{id: image, cached: old, unknown: cleanupImageFixture(unknown)}
	var out bytes.Buffer
	_ = svc.Cleanup(context.Background(), options, &out)
	if len(cleanupRemoved(fake)) != 0 {
		t.Fatal("label alone granted ownership")
	}
	cleanupArtifact(t, directory, "attempt-primary.json", map[string]any{"Version": 1, "Invocation": invocation, "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage", "Result": map[string]any{"ID": "", "Step": "COMMIT localhost/sandboxed-agents:none-" + asset, "Source": "podman --layer-label + exact image inspect", "Asset": asset, "LayerInvocation": invocation}})
	out.Reset()
	err := svc.Cleanup(context.Background(), options, &out)
	if got := cleanupRemoved(fake); len(got) != 1 || got[0] != id {
		t.Fatalf("primary intermediate not isolated: %v %v %s", got, err, &out)
	}
	if !strings.Contains(out.String(), cached) || !strings.Contains(out.String(), unknown) {
		t.Fatal("unproven IDs omitted")
	}
}

// TestStatusLeavesConsistentExitTextUntouched proves the status command seam repairs inconsistencies without rewriting already-authoritative plain status.
func TestStatusLeavesConsistentExitTextUntouched(t *testing.T) {
	svc, _, options := cleanupHarness(t)
	path := filepath.Join(options.RunDir, "exit-status")
	stamp := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), []string{"status", "-run-dir", options.RunDir}, &out, &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatal("status query rewrote consistent plain exit evidence")
	}
}

// TestCleanupContradictoryAssetScopeStillReports proves the Cleanup seam retains every image and emits after-measurement when primary invocation assets contradict each other.
func TestCleanupContradictoryAssetScopeStillReports(t *testing.T) {
	svc, fake, options := cleanupHarness(t)
	id := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("c", 64)
	fake.images = map[string]cleanupFakeImage{id: cleanupImageFixture(id), other: cleanupImageFixture(other)}
	svc.BuildEvidence = func(context.Context, string) ([]liverun.BuildResult, error) {
		return []liverun.BuildResult{{ID: id, Step: "primary", Source: "trusted fixture", Asset: strings.Repeat("e", 64)}, {ID: other, Step: "primary", Source: "trusted fixture", Asset: strings.Repeat("f", 64)}}, nil
	}
	var out bytes.Buffer
	err := svc.Cleanup(context.Background(), options, &out)
	if err == nil || len(cleanupRemoved(fake)) != 0 || !strings.Contains(out.String(), `"after"`) || !strings.Contains(out.String(), "trusted asset identity inconsistent") {
		t.Fatalf("contradictory scope lost audit or granted ownership: %v %s", err, &out)
	}
}
