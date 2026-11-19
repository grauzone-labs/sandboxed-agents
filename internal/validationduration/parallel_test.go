package validationduration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// TestCollectActualParallelVerboseOutput proves real parallel Go subtest output
// retains each fixed case once, preserves failing records, omits nested/program
// output, and refuses a duplicated completion at the public collector seam.
func TestCollectActualParallelVerboseOutput(t *testing.T) {
	command := exec.Command("go", "test", "-v", "-count=1", "./testdata/parallel")
	command.Env = testutil.Env(os.Environ(), map[string]string{testutil.LiveTestsEnv: "", "SANDBOXED_AGENTS_LIVE_IMAGES_TESTS": ""})
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "=== CONT") || !strings.Contains(string(output), "--- FAIL: TestLiveSuite/update") {
		t.Fatalf("parallel fixture: %v\n%s", err, output)
	}
	state := validationduration.Context{HostLoad: "unknown", BaseImage: "unknown", BuildCache: "unknown"}
	data, err := validationduration.Collect(strings.NewReader(string(output)), []byte(record), state)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Cases []struct{ Name, Result string }
	}
	if err = json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range evidence.Cases {
		got[item.Name] = item.Result
	}
	want := map[string]string{"preflight": "pass", "lifecycle": "pass", "update": "fail", "agent": "pass", "t3code": "pass", "images": "skip"}
	if len(evidence.Cases) != 6 || !reflect.DeepEqual(got, want) || strings.Contains(string(data), "private") {
		t.Fatalf("collected %s", data)
	}
	var duplicate string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "--- FAIL: TestLiveSuite/update (") {
			duplicate = line + "\n"
			break
		}
	}
	if duplicate == "" {
		t.Fatalf("no completion line: %s", output)
	}
	if _, err = validationduration.Collect(strings.NewReader(string(output)+duplicate), []byte(record), state); err == nil {
		t.Fatal("duplicate parallel case accepted")
	}
}
