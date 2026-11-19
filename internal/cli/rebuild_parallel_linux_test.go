package cli_test

import (
	"github.com/grauzone-labs/sandboxed-agents/image"
	"path/filepath"
	"testing"
)

// TestBuildJobsCapturesBaseWhileTagLocked proves a second rebuild cannot replace the refreshed base during its ID inspection at the subprocess CLI, fake Podman and Linux kernel lock seams.
func TestBuildJobsCapturesBaseWhileTagLocked(t *testing.T) {
	f := linuxHost(t)
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "inspecting"), filepath.Join(dir, "release")
	f.responses["image ls *"] = response{Output: `[]`}
	f.responses["build *"] = response{}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`, ReadyFile: ready, WaitFor: release}
	first := startConcurrentCommand(t, f, "default", "build", "--with", "native", "--jobs=2")
	t.Cleanup(func() { releaseHandshakeFile(t, release) })
	waitHandshakeFile(t, ready)
	next := startConcurrentCommand(t, f, "other", "build")
	waitHostLock(t, next, imageTagLockPath(baseTag(t)))
	if builds := countPodmanCalls(t, f, "build "); builds != 1 {
		t.Fatalf("second rebuild entered before base ID captured: %d", builds)
	}
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
}

// TestConcurrentBuildJobsToolchainRebuildBlocksUp proves an opted-in toolchain worker holds the shared tag lock through its build at the subprocess CLI/fake Podman seam.
func TestConcurrentBuildJobsToolchainRebuildBlocksUp(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "other", "demo")
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "building"), filepath.Join(dir, "release")
	f.responses["image ls *"] = response{Output: `[]`}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
	// An explicit known tag comes from the same public helper used by the caller's selected set.
	hash, err := image.Hash(image.Context())
	if err != nil {
		t.Fatal(err)
	}
	tag := image.Tag(hash, []string{"dotnet"})
	f.responses["image exists "+tag] = response{}
	f.responses["image inspect "+tag] = response{Output: `[{"Id":"toolchain-new","Labels":{"io.sandboxed-agents.base-image":"base-new"}}]`}
	f.responses["build *"] = response{}
	f.responses["build * --tag "+tag+" *"] = response{ReadyFile: ready, WaitFor: release}
	first := startConcurrentCommand(t, f, "default", "build", "--with", "dotnet", "--jobs=2")
	t.Cleanup(func() { releaseHandshakeFile(t, release) })
	waitHandshakeFile(t, ready)
	next := startWaitingUp(t, f, "other", "demo", imageTagLockPath(tag), "--with", "dotnet")
	if reads := countPodmanCalls(t, f, "image exists "+tag); reads != 0 {
		t.Fatalf("ensure read tag while worker was building: %d", reads)
	}
	releaseHandshakeFile(t, release)
	first.finish(t, 0)
	next.finish(t, 0)
	if builds := countPodmanCalls(t, f, "build "); builds != 2 {
		t.Fatalf("builds=%d want base and toolchain once", builds)
	}
}
