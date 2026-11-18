package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// concurrentCommand owns one concurrent CLI subprocess and its eventual combined output.
type concurrentCommand struct {
	command *exec.Cmd
	output  bytes.Buffer
	stderr  bytes.Buffer
	done    chan error
}

// startConcurrentCommand starts a real CLI boundary with independent group and fake responses.
func startConcurrentCommand(t *testing.T, f *fixture, group string, args ...string) *concurrentCommand {
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
	run := &concurrentCommand{command: exec.Command(program, "-test.run=^TestUpExecutableBoundary$"), done: make(chan error, 1)}
	run.command.Env = testutil.Env(os.Environ(), map[string]string{"TEST_UP_EXECUTABLE": "1", "TEST_UP_ARGS": string(arguments), "TEST_UP_FILES": string(files), "TEST_UP_PLATFORM": f.host.Platform, "TEST_PODMAN_RESPONSES": string(responses), "SANDBOXED_AGENTS_GROUP": group})
	run.command.Stdout, run.command.Stderr = &run.output, &run.stderr
	if err := run.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { run.done <- run.command.Wait() }()
	t.Cleanup(func() { _ = run.command.Process.Kill() })
	return run
}

// imageTagLockPath returns the host-state image-tag lock path specified by ADR-0007.
func imageTagLockPath(tag string) string {
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "images", fmt.Sprintf("%x", sha256.Sum256([]byte(tag))))
}

// startWaitingUp reaches the last sandbox-object query, then waits for an observable lock wait.
func startWaitingUp(t *testing.T, f *fixture, group, name, lockPath string, options ...string) *concurrentCommand {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "objects-read")
	key := "container exists sandboxed-agents-backup." + group + "." + name
	answer := f.responses[key]
	answer.ReadyFile = ready
	f.responses[key] = answer
	run := startConcurrentCommand(t, f, group, append([]string{"up", name}, options...)...)
	waitHandshakeFile(t, ready)
	waitHostLock(t, run, lockPath)
	return run
}

// finish checks the CLI result after a bounded wait and returns its complete output.
func (run *concurrentCommand) finish(t *testing.T, want int) string {
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
			t.Fatalf("status=%d want=%d output=%s", status, want, run.output.String()+run.stderr.String())
		}
		return run.output.String() + run.stderr.String()
	case <-time.After(8 * time.Second):
		t.Fatal("concurrent CLI did not finish")
		return ""
	}
}

// waitHandshakeFile waits for a fake Podman rendezvous without inspecting CLI internals.
func waitHandshakeFile(t *testing.T, path string) {
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

// releaseHandshakeFile releases a fake Podman boundary held by the test.
func releaseHandshakeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

// countPodmanCalls counts external calls beginning with prefix in a completed or idle fake log.
func countPodmanCalls(t *testing.T, f *fixture, prefix string) int {
	t.Helper()
	count := 0
	for _, call := range testutil.Invocations(t, f.fakes.PodmanLog) {
		if strings.HasPrefix(strings.Join(call, " "), prefix) {
			count++
		}
	}
	return count
}

// TestConcurrentImageRebuildBlocksUp proves a rebuild excludes up's same-tag ensure across controller
// groups, which rechecks the image after waiting, at the subprocess CLI/fake Podman seam.
func TestConcurrentImageRebuildBlocksUp(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	first := startConcurrentCommand(t, f, "default", "build")
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", imageTagLockPath(baseTag(t)))
	premature := countPodmanCalls(t, f, "image exists ")
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
	assertWaitingNotice(t, first, "")
	assertWaitingNotice(t, next, "image-tag")
	if premature != 0 {
		t.Fatalf("up inspected the tag during the rebuild: %d calls", premature)
	}
	if builds := countPodmanCalls(t, f, "build "); builds != 1 {
		t.Fatalf("builds=%d want 1", builds)
	}
}

// TestConcurrentUpBuildsDistinctToolchainTagsConcurrently proves up commands overlap builds of
// different toolchain tags with a prepared base image at the subprocess CLI/fake Podman seam.
func TestConcurrentUpBuildsDistinctToolchainTagsConcurrently(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "dotnet")
	scriptNewSandbox(t, f, "other", "azure")
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	release := filepath.Join(directory, "release")
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:base"}]`}
	for _, toolchain := range []string{"dotnet", "azure"} {
		tag := image.Tag(hash, []string{toolchain})
		f.responses["image exists "+tag] = response{Exit: 1}
		f.responses["build * --tag "+tag+" *"] = response{ReadyFile: filepath.Join(directory, toolchain), WaitFor: release}
	}
	f.responses[portInventory] = concurrentPortInventory()
	first := startConcurrentCommand(t, f, "default", "up", "dotnet", "--with", "dotnet")
	waitHandshakeFile(t, filepath.Join(directory, "dotnet"))
	next := startConcurrentCommand(t, f, "other", "up", "azure", "--with", "azure")
	// Both external builds must enter before either is released: serial builds cannot pass.
	waitHandshakeFile(t, filepath.Join(directory, "azure"))
	releaseHandshakeFile(t, release)
	outputs := first.finish(t, 0) + next.finish(t, 0)
	if builds := countPodmanCalls(t, f, "build "); builds != 2 {
		t.Errorf("builds=%d want one per toolchain tag", builds)
	}
	for _, port := range []string{"2222", "2223"} {
		if strings.Count(outputs, "127.0.0.1:"+port+":22/tcp") != 1 {
			t.Errorf("expected one claim of port %s: %s", port, outputs)
		}
	}
}

// TestConcurrentUpBuildsOnceAndClaimsDistinctPorts proves two up commands across controller groups
// wait on the same image tag, reuse its completed build, and claim distinct ports at the CLI seam.
func TestConcurrentUpBuildsOnceAndClaimsDistinctPorts(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	scriptNewSandbox(t, f, "other", "demo")
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	f.responses["image exists "+baseTag(t)] = response{Sequence: []response{{Exit: 1}, {}}}
	f.responses[portInventory] = concurrentPortInventory()
	first := startConcurrentCommand(t, f, "default", "up", "demo")
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", imageTagLockPath(baseTag(t)))
	premature := countPodmanCalls(t, f, portInventory)
	if checks := countPodmanCalls(t, f, "image exists "+baseTag(t)); checks != 1 {
		t.Errorf("image checks while first build holds the tag=%d want 1", checks)
	}
	releaseHandshakeFile(t, release)
	firstOutput := first.finish(t, 0)
	nextOutput := next.finish(t, 0)
	if premature != 2 {
		t.Errorf("early port inventories during first build=%d want 2", premature)
	}
	if builds := countPodmanCalls(t, f, "build "); builds != 1 {
		t.Errorf("builds=%d want 1", builds)
	}
	if checks := countPodmanCalls(t, f, "image exists "+baseTag(t)); checks != 2 {
		t.Errorf("image checks after completed build=%d want 2", checks)
	}
	if !strings.Contains(firstOutput, "127.0.0.1:2222:22/tcp") || !strings.Contains(nextOutput, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("port claims: first=%s next=%s", firstOutput, nextOutput)
	}
}

// TestConcurrentPortClaimLastsThroughCreate proves a concurrent up waits until the first container
// records its port claim, even with an already cached image, at the CLI/fake Podman seam.
func TestConcurrentPortClaimLastsThroughCreate(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	scriptNewSandbox(t, f, "other", "demo")
	ready, release := filepath.Join(t.TempDir(), "creating"), filepath.Join(t.TempDir(), "release")
	f.responses["create --name sandboxed-agents.default.demo *"] = response{ReadyFile: ready, WaitFor: release}
	f.responses[portInventory] = concurrentPortInventory()
	first := startConcurrentCommand(t, f, "default", "up", "demo")
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "ports"))
	premature := countPodmanCalls(t, f, portInventory)
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	output := next.finish(t, 0)
	assertWaitingNotice(t, first, "")
	assertWaitingNotice(t, next, "port-allocation")
	if premature != 3 {
		t.Errorf("port inventories before first creation completes=%d want 3 (two early, one locked)", premature)
	}
	if !strings.Contains(output, "127.0.0.1:2223:22/tcp") {
		t.Fatalf("second port claim=%s", output)
	}
}

// TestConcurrentImageFailureReleasesWaitingCommands proves every image-writing CLI path excludes a
// waiting up, then releases the tag on failure so up can report its own build failure at the CLI seam.
func TestConcurrentImageFailureReleasesWaitingCommands(t *testing.T) {
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
			ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
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
			first := startConcurrentCommand(t, f, "default", args...)
			waitHandshakeFile(t, ready)
			before := countPodmanCalls(t, f, "image exists ")
			next := startWaitingUp(t, f, "other", "next", imageTagLockPath(baseTag(t)))
			premature := countPodmanCalls(t, f, "image exists ") - before
			releaseHandshakeFile(t, release)
			firstOutput := first.finish(t, 1)
			nextOutput := next.finish(t, 1)
			if premature != 0 {
				t.Errorf("up inspected image before failed writer released it: %d", premature)
			}
			if builds := countPodmanCalls(t, f, "build "); builds != 2 {
				t.Errorf("build attempts=%d want 2", builds)
			}
			for _, output := range []string{firstOutput, nextOutput} {
				if !strings.Contains(output, "build failure for this command") {
					t.Errorf("missing own build diagnostic: %s", output)
				}
			}
			if creates := countPodmanCalls(t, f, "create "); creates != 0 {
				t.Errorf("created %d containers after build failure", creates)
			}
		})
	}
}

// TestConcurrentWaitKeepsSameSandboxRefusalImmediate proves an up waiting inside a shared step
// retains its nonblocking lifecycle lock and refuses another up without object reads at the CLI seam.
func TestConcurrentWaitKeepsSameSandboxRefusalImmediate(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["build *"] = response{ReadyFile: ready, WaitFor: release}
	first := startConcurrentCommand(t, f, "default", "build")
	waitHandshakeFile(t, ready)
	waiter := startWaitingUp(t, f, "default", "demo", imageTagLockPath(baseTag(t)))
	before := countPodmanCalls(t, f, "container exists ")
	next := startConcurrentCommand(t, f, "default", "up", "demo")
	output := next.finish(t, 1)
	if !strings.Contains(output, `step 3: sandbox "demo": another lifecycle or SSH setup command is in progress; retry`) {
		t.Fatalf("refusal=%s", output)
	}
	if after := countPodmanCalls(t, f, "container exists "); after != before {
		t.Fatalf("second command read sandbox objects: before=%d after=%d", before, after)
	}
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	waiter.finish(t, 0)
}

// TestConcurrentToolchainRebuildBlocksUp proves a toolchain-tag rebuild and ensure use the same
// lock, and up uses its completed result without building again, at the CLI/fake Podman seam.
func TestConcurrentToolchainRebuildBlocksUp(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	tag := image.Tag(hash, []string{"dotnet"})
	ready, release := filepath.Join(t.TempDir(), "building"), filepath.Join(t.TempDir(), "release")
	f.responses["image ls *"] = response{Output: "[]"}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:base"}]`}
	f.responses["image exists "+tag] = response{}
	f.responses["image inspect "+tag] = response{Output: `[{"Id":"sha256:toolchain","Labels":{"io.sandboxed-agents.base-image":"sha256:base"}}]`}
	f.responses["build *"] = response{}
	f.responses["build * --tag "+tag+" *"] = response{ReadyFile: ready, WaitFor: release}
	first := startConcurrentCommand(t, f, "default", "build", "--with", "dotnet")
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", imageTagLockPath(tag), "--with", "dotnet")
	premature := countPodmanCalls(t, f, "image exists "+tag)
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
	if premature != 0 {
		t.Errorf("up read toolchain tag during rebuild: %d", premature)
	}
	if builds := countPodmanCalls(t, f, "build "); builds != 2 {
		t.Errorf("builds=%d want base and toolchain once each", builds)
	}
}

// assertWaitingNotice checks that only a contended command reports one named lock on stderr.
func assertWaitingNotice(t *testing.T, run *concurrentCommand, kind string) {
	t.Helper()
	if strings.Contains(run.output.String(), "waiting for ") {
		t.Errorf("wait notice on stdout: %s", run.output.String())
	}
	diagnostic := run.stderr.String()
	if kind == "" {
		if diagnostic != "" {
			t.Errorf("uncontended stderr=%q want empty", diagnostic)
		}
		return
	}
	if strings.Count(diagnostic, "waiting for ") != 1 || !strings.Contains(diagnostic, "waiting for "+kind+" lock: ") {
		t.Errorf("waiter stderr=%q want one %s lock notice", diagnostic, kind)
	}
}

// TestUncontendedUpHasNoLockNotice proves a command alone preserves its empty stderr at the CLI seam.
func TestUncontendedUpHasNoLockNotice(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	run := startConcurrentCommand(t, f, "default", "up", "demo")
	run.finish(t, 0)
	assertWaitingNotice(t, run, "")
}

// TestConcurrentUpRefusesAlreadyClaimedExplicitPort proves both commands pass the early port
// check, then the waiting command refuses the published claim without creating at the CLI seam.
func TestConcurrentUpRefusesAlreadyClaimedExplicitPort(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "demo")
	scriptNewSandbox(t, f, "other", "demo")
	ready, release := filepath.Join(t.TempDir(), "creating"), filepath.Join(t.TempDir(), "release")
	f.responses["create --name sandboxed-agents.default.demo *"] = response{ReadyFile: ready, WaitFor: release}
	f.responses[portInventory] = concurrentPortInventory()
	first := startConcurrentCommand(t, f, "default", "up", "demo", "--port", "2222")
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "ports"), "--port", "2222")
	if inventories := countPodmanCalls(t, f, portInventory); inventories != 3 {
		t.Errorf("inventories before claim publication=%d want two early checks and holder's locked check", inventories)
	}
	if creates := countPodmanCalls(t, f, "create "); creates != 1 {
		t.Errorf("creates while claim held=%d want 1", creates)
	}
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	output := next.finish(t, 1)
	if !strings.Contains(output, "SSH port 2222 is already recorded on a sandbox") || strings.Contains(output, "--publish") {
		t.Errorf("claim refusal=%s", output)
	}
	if inventories := countPodmanCalls(t, f, portInventory); inventories != 4 {
		t.Errorf("inventories after claim publication=%d want 4", inventories)
	}
	if creates := countPodmanCalls(t, f, "create "); creates != 1 {
		t.Errorf("creates after refusal=%d want 1", creates)
	}
	if volumes := countPodmanCalls(t, f, "volume create "); volumes != 3 {
		t.Errorf("volume creations=%d want only holder's three volumes", volumes)
	}
	assertWaitingNotice(t, first, "")
	assertWaitingNotice(t, next, "port-allocation")
	wantDiagnostic := "waiting for port-allocation lock: " + filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "ports") + "\nSSH port 2222 is already recorded on a sandbox\n"
	if got := next.stderr.String(); got != wantDiagnostic {
		t.Errorf("stderr=%q want %q", got, wantDiagnostic)
	}
}

// TestHostLockPreparationNamesKinds proves malformed image-tag and port-allocation lock paths
// produce contextual errors with no waiting notice at the subprocess CLI/fake Podman seam.
func TestHostLockPreparationNamesKinds(t *testing.T) {
	for _, kind := range []string{"image-tag", "port-allocation"} {
		t.Run(kind, func(t *testing.T) {
			f := linuxHost(t)
			scriptNewSandbox(t, f, "default", "demo")
			path := imageTagLockPath(baseTag(t))
			if kind == "port-allocation" {
				path = filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "ports")
			}
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			run := startConcurrentCommand(t, f, "default", "up", "demo")
			run.finish(t, 1)
			if got, want := run.stderr.String(), kind+" lock path is not a regular file: "+path+"\n"; got != want {
				t.Errorf("stderr=%q want %q", got, want)
			}
			if creates := countPodmanCalls(t, f, "create "); creates != 0 {
				t.Errorf("created %d containers despite lock failure", creates)
			}
		})
	}
}

// concurrentPortInventory scripts both early port checks and the two locked rechecks,
// with the first command's claim visible only after its container creation completes.
func concurrentPortInventory() response {
	return response{Sequence: []response{
		{Output: "[]"}, {Output: "[]"}, {Output: "[]"},
		{Output: `[{"Names":["sandboxed-agents.default.demo"],"Labels":{"io.sandboxed-agents.owner":"default","io.sandboxed-agents.port":"2222"}}]`},
	}}
}
