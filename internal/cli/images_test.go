package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

func baseTag(t *testing.T) string {
	t.Helper()
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	return image.Tag(hash, nil)
}

// TestUpBuildsAbsentImage proves up builds a missing image without prior build and requests sandbox creation and start through RunWithHost and fake Podman.
func TestUpBuildsAbsentImage(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	tag := baseTag(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+tag] = response{Exit: 1}
	f.responses["build *"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "sandbox demo running") || !strings.Contains(output, tag) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	buildIndex := -1
	for i, call := range calls {
		if len(call) > 0 && call[0] == "build" {
			buildIndex = i
		}
	}
	if buildIndex < 1 {
		t.Fatalf("missing build: %v", calls)
	}
	if !reflect.DeepEqual(calls[buildIndex-1], []string{"image", "exists", tag}) {
		t.Fatalf("wrong build order: %v", calls)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls, buildIndex, "", tag)
}

// TestBuildRetainsExistingSandboxes proves explicit build uses the shared image builder and never queries or replaces old sandboxes through RunWithHost and fake Podman.
func TestBuildRetainsExistingSandboxes(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	f.responses["build *"] = response{}
	status, output, calls := f.run(t, "build")
	if status != 0 || !strings.Contains(output, baseTag(t)) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	if len(calls) != 3 || len(calls[2]) < 1 || calls[2][0] != "build" {
		t.Fatalf("build touched sandbox: %v", calls)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls, 2, "", baseTag(t))
}

// TestUpUsesPresentImage proves up skips build for an existing shared image through RunWithHost and fake Podman.
func TestUpUsesPresentImage(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	tag := baseTag(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+tag] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "sandbox demo running") || !strings.Contains(output, tag) {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "demo", tag, "", "2222", "8589934592", "4", "2048", "1073741824")
}

// TestUpExistingSandboxDoesNotRebuild proves existing sandboxes bypass image inspection and rebuild, preserving their image at the exported CLI seam.
func TestUpExistingSandboxDoesNotRebuild(t *testing.T) {
	f := linuxHost(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "existing sandbox lifecycle is not implemented (#14)") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	if len(calls) != 3 || !reflect.DeepEqual(calls[2], []string{"container", "exists", "sandboxed-agents.default.demo"}) {
		t.Fatalf("image inspected or changed: %v", calls)
	}
	noMutation(t, calls)
}

// TestImageQueryFailuresDoNotBuild proves only exit status 1 means absence and other image or container query failures cannot trigger builds through RunWithHost.
func TestImageQueryFailuresDoNotBuild(t *testing.T) {
	for _, kind := range []string{"container", "image"} {
		t.Run(kind, func(t *testing.T) {
			f := linuxHost(t)
			f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
			key := "container exists sandboxed-agents.default.demo"
			if kind == "image" {
				key = "image exists " + baseTag(t)
			}
			f.responses[key] = response{Output: "query unavailable", Exit: 125}
			status, output, calls := f.run(t, "up", "demo")
			if status != 1 || !strings.Contains(output, "query unavailable") {
				t.Fatalf("status=%d output=%q", status, output)
			}
			noMutation(t, calls)
		})
	}
}

// TestImageBuildFailureStopsUp proves a failed image build prevents new sandbox preparation through RunWithHost and fake Podman.
func TestImageBuildFailureStopsUp(t *testing.T) {
	f := linuxHost(t)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["build *"] = response{Output: "build rejected", Exit: 125}
	status, output, calls := f.run(t, "up", "demo")
	if status != 1 || !strings.Contains(output, "build rejected") || strings.Contains(output, "sandbox creation is not implemented") {
		t.Fatalf("status=%d output=%q", status, output)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls, len(calls)-1, "", baseTag(t))
}

// TestWindowsImageBuildUsesCheckedTarget proves every image and sandbox lookup and build keeps the preflight-selected connection through RunWithHost and fake Podman.
func TestWindowsImageBuildUsesCheckedTarget(t *testing.T) {
	f := windowsHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	f.responses["--connection chosen container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["--connection chosen image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["--connection chosen build *"] = response{}
	status, output, calls := f.run(t, "up", "demo")
	if status != 0 || !strings.Contains(output, "sandbox demo running") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	buildFound := false
	buildIndex := -1
	for index, call := range calls {
		if len(call) > 0 && call[0] == "build" {
			t.Fatalf("unbound build: %v", call)
		}
		if len(call) > 2 && call[0] == "--connection" && call[1] == "chosen" && call[2] == "build" {
			buildFound = true
			buildIndex = index
		}
	}
	if !buildFound {
		t.Fatalf("no bound build: %v", calls)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls, buildIndex, "chosen", baseTag(t))
}

// TestBuildAndUpAgreeOnNoneImageTag proves build and absent-image up select, build, and report the same tag for --with none through RunWithHost and fake Podman.
func TestBuildAndUpAgreeOnNoneImageTag(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	f.responses["build *"] = response{}
	status, output, calls := f.run(t, "build", "--with", "none")
	if status != 0 {
		t.Fatalf("build status=%d output=%q", status, output)
	}
	buildCall := calls[len(calls)-1]
	if len(buildCall) != 6 || buildCall[0] != "build" || buildCall[3] != "--tag" {
		t.Fatalf("build calls=%v", calls)
	}
	tag := buildCall[4]
	if tag != baseTag(t) || !strings.Contains(output, "built image "+tag) {
		t.Fatalf("build tag=%q output=%q", tag, output)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls, len(calls)-1, "", tag)
	previousCallCount := len(calls)
	f.responses["container exists sandboxed-agents.default.demo"] = response{Exit: 1}
	f.responses["image exists "+tag] = response{Exit: 1}
	_, output, calls = f.run(t, "up", "demo", "--with", "none")
	found := false
	buildIndex := -1
	for index, call := range calls[previousCallCount:] {
		if len(call) > 0 && call[0] == "build" {
			found = true
			if len(call) != 6 || call[3] != "--tag" || call[4] != tag {
				t.Fatalf("up built tag differs: %v", call)
			}
			absolute := previousCallCount + index
			buildIndex = index
			if absolute == 0 || !reflect.DeepEqual(calls[absolute-1], []string{"image", "exists", tag}) {
				t.Fatalf("up checked a different tag: %v", calls)
			}
		}
	}
	if !found || !strings.Contains(output, "image "+tag+";") {
		t.Fatalf("up tag=%q output=%q calls=%v", tag, output, calls)
	}
	assertExpectedBuildAndNoOtherMutations(t, calls[previousCallCount:], buildIndex, "", tag)
}

func assertExpectedBuildAndNoOtherMutations(t *testing.T, calls [][]string, buildIndex int, connection, tag string) {
	t.Helper()
	if buildIndex < 0 || buildIndex >= len(calls) {
		t.Fatalf("missing expected build: %v", calls)
	}
	prefix := []string{}
	if connection != "" {
		prefix = []string{"--connection", connection}
	}
	call := calls[buildIndex]
	if len(call) != len(prefix)+6 {
		t.Fatalf("unexpected build shape: %v", call)
	}
	context := call[len(call)-1]
	expected := append(append([]string{}, prefix...), "build", "--file", filepath.Join(context, "Containerfile"), "--tag", tag, context)
	if !filepath.IsAbs(context) || filepath.Base(context) != "base" || !reflect.DeepEqual(call, expected) {
		t.Fatalf("unexpected build arguments: %v; want %v", call, expected)
	}
	// Exempt only the validated build command itself, never its other arguments.
	arguments := append(append([]string{}, call[:len(prefix)]...), call[len(prefix)+1:]...)
	noMutation(t, [][]string{arguments})
	remaining := append(append([][]string{}, calls[:buildIndex]...), calls[buildIndex+1:]...)
	if len(calls) > 1 && calls[len(calls)-2][len(calls[len(calls)-2])-2] == "start" {
		assertNewSandbox(t, calls, connection, "default", "demo", tag, "", "2222", "8589934592", "4", "2048", "1073741824")
		remaining = remaining[:len(remaining)-9]
	}
	noMutation(t, remaining)
}

// TestMutationAssertionRejectsConnectionPrefixedCreate proves a connection flag cannot hide a create operation from the test guard through an isolated test process.
func TestMutationAssertionRejectsConnectionPrefixedCreate(t *testing.T) {
	const childEnv = "TEST_CLI_MUTATION_GUARD_CHILD"
	if os.Getenv(childEnv) == "1" {
		noMutation(t, [][]string{{"--connection", "chosen", "create", "sandboxed-agents.default.demo"}})
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestMutationAssertionRejectsConnectionPrefixedCreate$")
	command.Env = testutil.Env(os.Environ(), map[string]string{childEnv: "1"})
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unexpected mutation") {
		t.Fatalf("mutation guard accepted connection-prefixed create: error=%v output=%s", err, output)
	}
}
