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
)

// TestCleanupRejectsForgedBuildOutput proves Cleanup retains foreign and unscoped results at the engine boundary.
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

// TestPrepareRejectsLockRootOverride proves command callers cannot select a second coordination namespace.
func TestPrepareRejectsLockRootOverride(t *testing.T) {
	svc, _, args, _ := commandHarness(t)
	args = append(args, "-lock-root", t.TempDir())
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err == nil {
		t.Fatal("public lock root bypass accepted")
	}
}

// TestPrepareRejectsUntrackedAndIgnoredInputs proves git source status stops preparation before any build.
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

// TestCleanupConsumesPrimaryBuildChannel proves iidfile publication reaches cleanup without granting authority to forged suite output.
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

// TestRunAndCleanupRejectDifferentStoredCoordinationRoot proves legacy private options cannot split exclusion between phases.
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

// TestCollectSeparatesRetainedCasesFromOptionalEvidence proves current case durations stay confirmed while invalid optional evidence is surfaced privately.
func TestCollectSeparatesRetainedCasesFromOptionalEvidence(t *testing.T) {
	svc, fake, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	cleanupArtifact(t, run, "exit.json", map[string]any{"Code": 0})
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

// TestPrivateLogFailureRemainsUnverified proves status preserves the suite code while collect and cleanup refuse incomplete log evidence.
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
