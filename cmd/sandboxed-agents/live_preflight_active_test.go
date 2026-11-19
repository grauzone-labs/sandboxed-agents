package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
	"github.com/grauzone-labs/sandboxed-agents/internal/release"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveTimingActiveIntegration proves actual source-built check/list/remove
// calls join an active ledger under overlap2 with separate complete P/S sums,
// T/core operations and schema2, through the nested harness and real Retain seam.
func TestLiveTimingActiveIntegration(t *testing.T) {
	root := t.TempDir()
	output, err := timingChild(t, "TestLiveTimingActiveFixture", root)
	if err != nil {
		t.Fatalf("active fixture: %v\n%s", err, output)
	}
	directory := filepath.Join(root, "records")
	evidence := readTimingOperations(t, directory)
	if evidence.CaseOverlap != 2 || evidence.SuiteSeconds == nil || *evidence.SuiteSeconds <= 0 {
		t.Fatalf("run settings/T lost: %+v", evidence)
	}
	before, err := os.ReadFile(filepath.Join(directory, "live-suite-"+livePlatform()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record release.LiveSuiteRecord
	if err := json.Unmarshal(before, &record); err != nil {
		t.Fatal(err)
	}
	if record.Result != "pass" || len(record.Checks) != 6 {
		t.Fatalf("suite verdict/schema changed: %+v", record)
	}
	for index, item := range record.Checks {
		if item.Name != liveCheckNames[index] {
			t.Fatal("fixed schema2 order changed")
		}
		if index < 3 && item.Result != "pass" || index >= 3 && item.Result != "not-run" {
			t.Fatalf("fixture case coverage misrepresented: %+v", record.Checks)
		}
	}
	for _, item := range evidence.Cases {
		if item.Seconds > *evidence.SuiteSeconds {
			t.Fatal("T omitted a complete case")
		}
	}
	recordPath := filepath.Join(directory, "live-suite-"+livePlatform()+".json")
	if _, err := validationduration.Retain(bytes.NewReader(output), recordPath, validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown", PreflightDir: filepath.Join(root, "preflight-timing")}); err != nil {
		t.Fatal(err)
	}
	evidence = readTimingOperations(t, directory)
	summary := evidence.Preflight
	if summary == nil || summary.State != "complete" || len(summary.Reasons) != 0 || summary.Invocations != 7 || summary.Preflight.Measured != 2 || summary.TargetSelection.Measured != 4 || summary.Preflight.Seconds == nil || summary.TargetSelection.Seconds == nil {
		t.Fatalf("active complete P/S missing: %+v", summary)
	}
	// Independently join the actual private observations, rather than inferring
	// preflight time from inclusive operation classes or case/Go output durations.
	journal, err := preflighttiming.Read(filepath.Join(root, "preflight-timing"), evidence.Commit)
	if err != nil {
		t.Fatal(err)
	}
	p, s := 0.0, 0.0
	for index, entry := range journal.Entries {
		if entry.Launch == nil || entry.Invocation == nil || summary.Rows[index].Sequence != index+1 {
			t.Fatal("launch-order correlation lost")
		}
		if phase := entry.Invocation.Preflight; phase != nil {
			p += *phase.Seconds
		}
		if phase := entry.Invocation.TargetSelection; phase != nil {
			s += *phase.Seconds
		}
	}
	if p <= 0 || s <= 0 || *summary.Preflight.Seconds != p || *summary.TargetSelection.Seconds != s {
		t.Fatalf("scope sums combined or inferred: P%f S%f summary%+v", p, s, summary)
	}
	if after, err := os.ReadFile(recordPath); err != nil || !bytes.Equal(before, after) {
		t.Fatal("Retain changed schema2")
	}
}

// TestLiveTimingActiveFixture exercises two actual full preflight calls plus
// concurrent target-only reads and real isolated missing-sandbox removals. The
// synthetic base-ready signal selects the scheduler seam without building images.
func TestLiveTimingActiveFixture(t *testing.T) {
	suite, root, fakes := timingFixture(t)
	responses := map[string]lifecycleResponse{
		"--version":                     {Output: "podman version 5.0.0"},
		"*info --format json":           {Output: `{"host":{"cpus":2,"memTotal":1073741824,"security":{"rootless":true},"cgroupVersion":"v2"}}`},
		"machine list --format json":    {Output: `[{"Name":"fixture","Default":true,"VMType":"wsl"}]`},
		"machine inspect fixture":       {Output: `[{"Name":"fixture","State":"running","Rootful":false,"SSHConfig":{"IdentityPath":` + quoteLiveJSON(filepath.Join(fakes.Dir, "key")) + `,"Port":2222,"RemoteUsername":"core"}}]`},
		"*version --format json":        {Output: `{"Server":{"Version":"5.0.0"}}`},
		"*ps --all --format json":       {Output: "[]"},
		"*volume ls --format json":      {Output: "[]"},
		"*ps --all --format {{.Names}}": {},
		"*volume ls --format {{.Name}}": {},
		"* exists *":                    {Exit: 1},
	}
	script, _ := json.Marshal(responses)
	ssh, _ := json.Marshal(map[string]lifecycleResponse{"* id -u": {Output: "1000\n"}, "* cat /proc/self/cgroup": {Output: "0::/user.slice/user-1000.slice/session-1.scope\n"}, "* cat *cgroup.controllers": {Output: "cpu memory pids"}, "* cat *cgroup.subtree_control": {Output: "cpu memory pids"}, "* test -e /etc/wsl.conf": {Exit: 1}})
	suite.host.environment = testutil.Env(suite.host.environment, map[string]string{"TEST_PODMAN_RESPONSES": string(script), "TEST_SSH_RESPONSES": string(ssh)})
	suite.host.removeGroupStateOnCleanup(t)
	suite.host.assertNoGroupObjectsOnCleanup(t)
	// Actual host facts can make offline preflight fail. A returned failure is a
	// measured call; this fixture proves observation completeness, not host readiness.
	suite.run(t, "preflight", func(t *testing.T) { _, _ = suite.host.cli("", "check"); suite.host.mustCLI(t, "", "version") })
	first, second, release := filepath.Join(root, "first-ready"), filepath.Join(root, "second-ready"), filepath.Join(root, "release")
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer os.WriteFile(release, nil, 0600)
		deadline := time.Now().Add(30 * time.Second)
		for {
			_, left := os.Stat(first)
			_, right := os.Stat(second)
			if left == nil && right == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Error("overlap2 did not reach both blocked fake engine reads")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	runRead := func(t *testing.T, local *liveSuite, name, ready string) {
		local.host.cleanupSandbox(t, name)
		// Each case has its own scripted handshake, preserving the shared host env.
		localResponses := make(map[string]lifecycleResponse, len(responses))
		for key, value := range responses {
			localResponses[key] = value
		}
		localResponses["*ps --all --format json"] = lifecycleResponse{Output: "[]", ReadyFile: ready, WaitFor: release}
		data, _ := json.Marshal(localResponses)
		local.host.environment = testutil.Env(local.host.environment, map[string]string{"TEST_PODMAN_RESPONSES": string(data)})
		local.host.assertListed(t, name, "")
	}
	suite.runConcurrent(t, []liveCase{
		{"lifecycle", func(t *testing.T, local *liveSuite) {
			_, _ = local.host.cli("", "check")
			local.baseEnsured()
			runRead(t, local, "timing-first", first)
		}},
		{"update", func(t *testing.T, local *liveSuite) { runRead(t, local, "timing-second", second) }},
	}, nil)
	<-joined
	// The real cleanup helper must retain both forced removals and all per-sandbox
	// absence probes. Group inventory and host-state cleanup run before final T.
	removals := 0
	for _, operation := range suite.host.timings.evidence.Operations {
		if operation.Source == "cli" && operation.Operation == "cleanup" {
			removals++
		}
	}
	if removals != 2 {
		t.Fatalf("case cleanup invocations lost: %d", removals)
	}
	if !strings.HasPrefix(os.Getenv("XDG_STATE_HOME"), fakes.Dir) || !strings.HasPrefix(os.Getenv("HOME"), fakes.Dir) {
		t.Fatal("fixture host state escaped fake root")
	}
}
