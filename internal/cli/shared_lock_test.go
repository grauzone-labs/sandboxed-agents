package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// sharedCommand owns one concurrent CLI subprocess and its eventual combined output.
type sharedCommand struct {
	command *exec.Cmd
	output  bytes.Buffer
	done    chan error
}

// startSharedCommand starts a real CLI boundary with independent group and fake responses.
func startSharedCommand(t *testing.T, f *fixture, group string, args ...string) *sharedCommand {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	files, err := json.Marshal(f.files)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	run := &sharedCommand{command: exec.Command(program, "-test.run=^TestUpExecutableBoundary$"), done: make(chan error, 1)}
	run.command.Env = testutil.Env(os.Environ(), map[string]string{"TEST_UP_EXECUTABLE": "1", "TEST_UP_ARGS": string(arguments), "TEST_UP_FILES": string(files), "TEST_UP_PLATFORM": f.host.Platform, "TEST_PODMAN_RESPONSES": string(responses), "SANDBOXED_AGENTS_GROUP": group})
	run.command.Stdout, run.command.Stderr = &run.output, &run.output
	if err := run.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { run.done <- run.command.Wait() }()
	t.Cleanup(func() { _ = run.command.Process.Kill() })
	return run
}

// finish checks the CLI result after a bounded wait and returns its complete output.
func (run *sharedCommand) finish(t *testing.T, want int) string {
	t.Helper()
	select {
	case err := <-run.done:
		status := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				status = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if status != want {
			t.Fatalf("status=%d want=%d output=%s", status, want, run.output.String())
		}
		return run.output.String()
	case <-time.After(8 * time.Second):
		t.Fatal("concurrent CLI did not finish")
		return ""
	}
}

// waitSharedFile waits for a fake Podman rendezvous without inspecting CLI internals.
func waitSharedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake Podman never reached %s", path)
}

// releaseSharedFile releases a fake Podman boundary held by the test.
func releaseSharedFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

// sharedCalls counts external calls beginning with prefix in a completed or idle fake log.
func sharedCalls(t *testing.T, f *fixture, prefix string) int {
	t.Helper()
	count := 0
	for _, call := range testutil.Invocations(t, f.fakes.PodmanLog) {
		if strings.HasPrefix(strings.Join(call, " "), prefix) {
			count++
		}
	}
	return count
}

// TestSharedImageRebuildBlocksUp proves a rebuild excludes up's same-tag ensure across controller
// groups, which rechecks the image after waiting, at the subprocess CLI/fake Podman seam.
func TestSharedImageRebuildBlocksUp(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	ready, release, second := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "preflight")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	first := startSharedCommand(t, f, "default", "build")
	waitSharedFile(t, ready)
	info := f.responses["info --format json"]
	info.ReadyFile = second
	f.responses["info --format json"] = info
	next := startSharedCommand(t, f, "other", "up", "demo")
	waitSharedFile(t, second)
	time.Sleep(200 * time.Millisecond)
	premature := sharedCalls(t, f, "image exists ")
	releaseSharedFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
	if premature != 0 {
		t.Fatalf("up inspected the tag during the rebuild: %d calls", premature)
	}
	if builds := sharedCalls(t, f, "build "); builds != 1 {
		t.Fatalf("builds=%d want 1", builds)
	}
}

// TestSharedUpBuildsOnceAndClaimsDistinctPorts proves two up commands across controller groups
// wait before reading ports, build one missing image, and create distinct port claims at the CLI seam.
func TestSharedUpBuildsOnceAndClaimsDistinctPorts(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	scriptNewSandbox(t, f, "other", "demo")
	ready, release, second := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "preflight")
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	f.responses["image exists "+baseTag(t)] = response{Sequence: []response{{Exit: 1}, {}}}
	f.responses[portInventory] = response{Sequence: []response{{Output: "[]"}, {Output: `[{"Names":["sandboxed-agents.default.demo"],"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}}]`}}}
	first := startSharedCommand(t, f, "default", "up", "demo")
	waitSharedFile(t, ready)
	info := f.responses["info --format json"]
	info.ReadyFile = second
	f.responses["info --format json"] = info
	next := startSharedCommand(t, f, "other", "up", "demo")
	waitSharedFile(t, second)
	time.Sleep(200 * time.Millisecond)
	premature := sharedCalls(t, f, portInventory)
	releaseSharedFile(t, release)
	firstOutput := first.finish(t, 0)
	nextOutput := next.finish(t, 0)
	if premature != 1 {
		t.Errorf("port inventories during first build=%d want 1", premature)
	}
	if builds := sharedCalls(t, f, "build "); builds != 1 {
		t.Errorf("builds=%d want 1", builds)
	}
	if !strings.Contains(firstOutput, "127.0.0.1:2222:22/tcp") || !strings.Contains(nextOutput, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("port claims: first=%s next=%s", firstOutput, nextOutput)
	}
}

// TestSharedPortClaimLastsThroughCreate proves a concurrent up waits until the first container
// records its port claim, even with an already cached image, at the CLI/fake Podman seam.
func TestSharedPortClaimLastsThroughCreate(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	scriptNewSandbox(t, f, "other", "demo")
	ready, release, second := filepath.Join(t.TempDir(), "creating"), filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "preflight")
	f.responses["create --name sandboxed-agents.default.demo *"] = response{ReadyFile: ready, WaitFor: release}
	f.responses[portInventory] = response{Sequence: []response{{Output: "[]"}, {Output: `[{"Names":["sandboxed-agents.default.demo"],"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}}]`}}}
	first := startSharedCommand(t, f, "default", "up", "demo")
	waitSharedFile(t, ready)
	info := f.responses["info --format json"]
	info.ReadyFile = second
	f.responses["info --format json"] = info
	next := startSharedCommand(t, f, "other", "up", "demo")
	waitSharedFile(t, second)
	time.Sleep(200 * time.Millisecond)
	premature := sharedCalls(t, f, portInventory)
	releaseSharedFile(t, release)
	first.finish(t, 0)
	output := next.finish(t, 0)
	if premature != 1 {
		t.Errorf("port inventories before first creation completes=%d want 1", premature)
	}
	if !strings.Contains(output, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("second port claim=%s", output)
	}
}

// TestSharedImageFailureReleasesWaitingCommands proves every image-writing CLI path excludes a
// waiting up, then releases the tag on failure so up can report its own build failure at the CLI seam.
func TestSharedImageFailureReleasesWaitingCommands(t *testing.T) {
	for _, writer := range []string{"build", "up", "update", "update-all"} {
		t.Run(writer, func(t *testing.T) {
			var f *fixture
			switch writer {
			case "update":
				f = replacementFixture(t)
			case "update-all":
				f = batchUpdateFixture(t, "demo")
			default:
				f = linuxHost(t)
				scriptNewSandbox(t, f, "default", "demo")
			}
			scriptNewSandbox(t, f, "other", "next")
			if writer == "update-all" {
				f.responses[portInventory] = response{Output: `[{"Names":["sandboxed-agents.default.demo"],"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}}]`}
			}
			ready, release, second := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "preflight")
			f.responses["image exists "+baseTag(t)] = response{Exit: 1}
			f.responses["image ls *"] = response{Output: "[]"}
			f.responses["build *"] = response{ReadyFile: ready, WaitFor: release, Exit: 42, Stderr: "build failure for this command"}
			args := []string{writer, "demo"}
			if writer == "build" {
				args = []string{"build"}
			}
			if writer == "update-all" {
				args = []string{"update", "--all"}
			}
			first := startSharedCommand(t, f, "default", args...)
			waitSharedFile(t, ready)
			before := sharedCalls(t, f, "image exists ")
			info := f.responses["info --format json"]
			info.ReadyFile = second
			f.responses["info --format json"] = info
			next := startSharedCommand(t, f, "other", "up", "next")
			waitSharedFile(t, second)
			time.Sleep(200 * time.Millisecond)
			premature := sharedCalls(t, f, "image exists ") - before
			releaseSharedFile(t, release)
			firstOutput := first.finish(t, 1)
			nextOutput := next.finish(t, 1)
			if premature != 0 {
				t.Errorf("up inspected image before failed writer released it: %d", premature)
			}
			if builds := sharedCalls(t, f, "build "); builds != 2 {
				t.Errorf("build attempts=%d want 2", builds)
			}
			for _, output := range []string{firstOutput, nextOutput} {
				if !strings.Contains(output, "build failure for this command") {
					t.Errorf("missing own build diagnostic: %s", output)
				}
			}
			if creates := sharedCalls(t, f, "create "); creates != 0 {
				t.Errorf("created %d containers after build failure", creates)
			}
		})
	}
}

// TestSharedWaitKeepsSameSandboxRefusalImmediate proves an up waiting inside a shared step
// retains its nonblocking lifecycle lock and refuses another up without object reads at the CLI seam.
func TestSharedWaitKeepsSameSandboxRefusalImmediate(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["image exists "+baseTag(t)] = response{Exit: 1}
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	first := startSharedCommand(t, f, "default", "up", "demo")
	waitSharedFile(t, ready)
	before := sharedCalls(t, f, "container exists ")
	next := startSharedCommand(t, f, "default", "up", "demo")
	output := next.finish(t, 1)
	if !strings.Contains(output, `step 3: sandbox "demo": another lifecycle or SSH setup command is in progress; retry`) {
		t.Fatalf("refusal=%s", output)
	}
	if after := sharedCalls(t, f, "container exists "); after != before {
		t.Fatalf("second command read sandbox objects: before=%d after=%d", before, after)
	}
	releaseSharedFile(t, release)
	first.finish(t, 0)
}

// TestSharedToolchainRebuildBlocksUp proves a toolchain-tag rebuild and ensure use the same
// lock, and up uses its completed result without building again, at the CLI/fake Podman seam.
func TestSharedToolchainRebuildBlocksUp(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	tag := image.Tag(hash, []string{"dotnet"})
	ready, release, second := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "preflight")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:base"}]`}
	f.responses["image exists "+tag] = response{}
	f.responses["image inspect "+tag] = response{Output: `[{"Id":"sha256:toolchain","Labels":{"io.sandboxed-agents.base-image":"sha256:base"}}]`}
	f.responses["build *"] = response{}
	f.responses["build * --tag "+tag+" *"] = response{ReadyFile: ready, WaitFor: release}
	first := startSharedCommand(t, f, "default", "build", "--with", "dotnet")
	waitSharedFile(t, ready)
	info := f.responses["info --format json"]
	info.ReadyFile = second
	f.responses["info --format json"] = info
	next := startSharedCommand(t, f, "other", "up", "demo", "--with", "dotnet")
	waitSharedFile(t, second)
	time.Sleep(200 * time.Millisecond)
	premature := sharedCalls(t, f, "image exists "+tag)
	releaseSharedFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
	if premature != 0 {
		t.Errorf("up read toolchain tag during rebuild: %d", premature)
	}
	if builds := sharedCalls(t, f, "build "); builds != 2 {
		t.Errorf("builds=%d want base and toolchain once each", builds)
	}
}
