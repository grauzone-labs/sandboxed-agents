package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestLiveEngineFailureRollbackFixture exercises each real rollback assertion
// and its cleanup inside a recorded case at the fake CLI/process seam.
func TestLiveEngineFailureRollbackFixture(t *testing.T) {
	caseName := os.Getenv("TEST_LIVE_ENGINE_ROLLBACK_CASE")
	if caseName == "" {
		return
	}
	suite := newLiveSuite(t)
	suite.host.binary = os.Getenv(livePreparedExecutableEnv)
	suite.assetHash = tracerAssetHash
	body := map[string]func(*testing.T){"update": suite.update, "t3code": suite.t3code}[caseName]
	if body == nil {
		t.Fatal("unknown rollback fixture case")
	}
	suite.run(t, caseName, func(t *testing.T) {
		// Fatal runs defers before cleanups. Register this check last so it runs
		// before the real case's already-registered sandbox removal.
		defer func() {
			t.Cleanup(func() {
				evidence := readEngineFailureEvidence(t, suite.host)
				if len(evidence.Samples) != 1 {
					t.Errorf("rollback snapshot not persisted before cleanup: %+v", evidence.Samples)
				}
			})
		}()
		body(t)
	})
}

// TestLiveEngineFailureRollbackMismatchKeepsCaseVerdict proves a wrong failure
// or unexpected success at both real rollback assertions is sampled before
// cleanup, retaining the original assertion, case, schema and invocation timings
// through the nested recorded suite and fake CLI/Podman seams.
func TestLiveEngineFailureRollbackMismatchKeepsCaseVerdict(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the existing Tool tracer exercises its raw pseudo-terminal on Linux")
	}
	for _, caseName := range []string{"update", "t3code"} {
		for _, exit := range []int{23, 0} {
			label := "wrong-failure"
			if exit == 0 {
				label = "unexpected-success"
			}
			t.Run(caseName+"/"+label, func(t *testing.T) {
				fakes := testutil.Install(t, fakeExecutable)
				t.Setenv("PATH", fakes.Dir)
				directory := t.TempDir()
				program := filepath.Join(t.TempDir(), "sandboxed-agents")
				data, err := os.ReadFile(fakeExecutable)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(program, data, 0700); err != nil {
					t.Fatal(err)
				}
				name, set, assertion := "live-update", "native", "forced rollback:"
				executableResponses := map[string]lifecycleResponse{
					"up live-update --with native": {}, "update live-update --with none": {Output: "updated"},
					"list": {Output: tracerListing("live-update\trunning\t" + tracerPort + "\tvolume\tnone\t-\t3\t-\n")},
				}
				podmanResponses := map[string]lifecycleResponse{
					"* exists *":                     {Exit: 1},
					"exec --user=0:0 * ssh-host-key": {Output: tracerHostKey + "\n"}, "exec --user=0:0 * ssh-authorize *": {},
					"exec --user=0:0 * sh -c cat /proc/1/comm; cat /proc/1/status": {Output: "sshd\nName:\tsshd\nUid:\t0\t0\t0\t0\n"},
					"exec --user=1000:1000 * cat /sys/fs/cgroup/memory.swap.max":   {Output: "0\n"},
					"info --format json": {Output: `{"host":{"cpus":4,"memTotal":100,"memFree":0,"swapTotal":0,"swapFree":0}}`},
					"container inspect " + cli.DerivedNames(tracerGroup, "live-update").Container: {Sequence: []scriptedAnswer{
						{Output: tracerContainer("native", 1)}, {Output: tracerContainer("none", 1)}, {Output: tracerContainer("none", 1)},
					}},
				}
				if caseName == "t3code" {
					tracerToolResponses(t, executableResponses, podmanResponses, false)
					name, set, assertion = "live-t3code", "none", "forced sandbox rollback:"
				}
				forced := "update " + name + " --with " + set + " --force"
				executableResponses[forced] = lifecycleResponse{Exit: exit, Output: "cannot allocate memory; private-original"}
				executableResponses["remove "+name+" --volumes --force"] = lifecycleResponse{}
				executableScript, _ := json.Marshal(executableResponses)
				podmanScript, _ := json.Marshal(podmanResponses)
				values := nestedExecutables()
				for key, value := range map[string]string{
					"TEST_LIVE_ENGINE_ROLLBACK_CASE": caseName, "SANDBOXED_AGENTS_GROUP": tracerGroup, liveRecordDirEnv: directory, livePreparedExecutableEnv: program,
					"TEST_SANDBOXED_AGENTS_EXECUTABLE": program, "TEST_SANDBOXED_AGENTS_LOG": filepath.Join(directory, "commands.jsonl"), "TEST_SANDBOXED_AGENTS_RESPONSES": string(executableScript), "TEST_PODMAN_RESPONSES": string(podmanScript),
					"TEST_SSH_OUTPUT": "debug1: SSH2_MSG_NEWKEYS received\n", "TEST_SSH_EXIT": "255", "TEST_SSH_KNOWN_HOSTS": "[127.0.0.1]:" + tracerPort + " " + tracerHostKey + "\n",
				} {
					values[key] = value
				}
				command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLiveEngineFailureRollbackFixture$")
				command.Env = testutil.Env(fakes.Env(os.Environ()), values)
				output, err := command.CombinedOutput()
				if err == nil || strings.Contains(string(output), "rollback snapshot not persisted before cleanup") || !strings.Contains(string(output), assertion) || !strings.Contains(string(output), "cannot allocate memory; private-original") || (exit != 0 && !strings.Contains(string(output), "exit status 23")) || (exit == 0 && !strings.Contains(string(output), assertion+" <nil>")) {
					t.Fatalf("original rollback assertion changed: %v\n%q", err, output)
				}
				data, err = os.ReadFile(filepath.Join(directory, "engine-failures-linux.json"))
				if err != nil {
					t.Fatal(err)
				}
				var evidence liveEngineFailureEvidence
				if err := json.Unmarshal(data, &evidence); err != nil || len(evidence.Samples) != 1 || evidence.SamplesTruncated {
					t.Fatalf("unexpected rollback not sampled once: %s %v", data, err)
				}
				sample := evidence.Samples[0]
				if sample.Case != caseName || sample.Source != "cli" || sample.Operation != "update or rollback" || sample.Result != "fail" || sample.EngineQuery != "verified" || sample.CPUs == nil || *sample.CPUs != 4 || strings.Contains(string(data), "private-original") {
					t.Fatalf("rollback attribution/facts lost or output leaked: %s", data)
				}
				calls := testutil.Invocations(t, fakes.PodmanLog)
				queries := 0
				for _, call := range calls {
					if slices.Equal(call, []string{"info", "--format", "json"}) {
						queries++
					}
				}
				commands := testutil.Invocations(t, filepath.Join(directory, "commands.jsonl"))
				if queries != 1 || strings.Join(commands[len(commands)-1], " ") != "remove "+name+" --volumes --force" {
					t.Fatalf("missing cleanup or unexpected query count: %d %v", queries, commands)
				}
				data, err = os.ReadFile(filepath.Join(directory, liveRecordFilename(t)))
				if err != nil {
					t.Fatal(err)
				}
				var record map[string]json.RawMessage
				if err := json.Unmarshal(data, &record); err != nil || len(record) != 9 || string(record["schema_version"]) != "2" || string(record["result"]) != `"fail"` || !strings.Contains(string(record["checks"]), `{"name":"`+caseName+`","result":"fail"}`) {
					t.Fatalf("original schema/case verdict changed: %s %v", data, err)
				}
				data, err = os.ReadFile(filepath.Join(directory, "operations-linux.json"))
				if err != nil {
					t.Fatal(err)
				}
				var timings validationduration.Operations
				if err := json.Unmarshal(data, &timings); err != nil {
					t.Fatal(err)
				}
				counts, rollbacks := map[string]int{}, 0
				for _, operation := range timings.Operations {
					counts[operation.Source]++
					if operation.Source == "cli" && operation.Operation == "update or rollback" {
						rollbacks++
						if rollbacks == 2 && operation.Result != map[bool]string{true: "pass", false: "fail"}[exit == 0] {
							t.Fatalf("mismatch changed invocation result: %+v", operation)
						}
					}
				}
				if counts["cli"] != len(commands) || counts["podman"] != len(calls) || rollbacks != 2 {
					t.Fatalf("invocation timings lost: %v; cli=%d podman=%d rollbacks=%d", counts, len(commands), len(calls), rollbacks)
				}
			})
		}
	}
}
