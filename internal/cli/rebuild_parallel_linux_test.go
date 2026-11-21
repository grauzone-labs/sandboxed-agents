package cli_test

import (
	"bytes"
	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"os"
	"path/filepath"
	"strings"
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

// cleanupInterferenceOutput replaces one owned spool path with a nonempty directory after its engine has finished, exercising the real removal boundary.
type cleanupInterferenceOutput struct {
	directory string
	retained  string
	setupErr  error
	output    bytes.Buffer
}

// Write preserves caller output while making the selected spool's exact-path removal fail without any production I/O injection.
func (w *cleanupInterferenceOutput) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "build output for toolchain set azure:") {
		paths, err := filepath.Glob(filepath.Join(w.directory, "sandboxed-agents-build-output-*"))
		if err == nil {
			for _, path := range paths {
				contents, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Contains(contents, []byte("begin azure\n")) {
					continue
				}
				err = os.Rename(path, path+".retained")
				if err == nil {
					err = os.Mkdir(path, 0700)
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(path, "external-entry"), []byte("keep"), 0600)
				}
				if err == nil {
					w.retained = path
				}
				break
			}
		}
		w.setupErr = err
	}
	return w.output.Write(data)
}

// TestBuildJobsSpoolCleanupFailureKeepsRebuiltTag proves a real removal failure reports I/O without misclassifying a replaced tag or deleting unrelated contents at RunWithHost/fake Podman/filesystem seams.
func TestBuildJobsSpoolCleanupFailureKeepsRebuiltTag(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet")
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	for _, name := range []string{"none", "azure", "dotnet"} {
		releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
	}
	stdout := &cleanupInterferenceOutput{directory: temp}
	var stderr bytes.Buffer
	status := cli.RunWithHost([]string{"build", "--jobs=2"}, stdout, &stderr, f.host)
	if stdout.setupErr != nil || stdout.retained == "" {
		t.Fatalf("cleanup failure fixture was not established: path=%q err=%v", stdout.retained, stdout.setupErr)
	}
	if status != 1 || strings.Contains(stderr.String(), "failed toolchain sets") || !strings.Contains(stderr.String(), "remove build output spool") || !strings.Contains(stderr.String(), "was rebuilt") || !strings.Contains(stderr.String(), "existing sandboxes keep their image until update; list marks them as outdated") {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	for _, name := range []string{"none", "azure", "dotnet"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".tag"))
		if err != nil || string(data) != "new-"+name {
			t.Fatalf("%s tag=%s err=%v", name, data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(stdout.retained, "external-entry"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("unrelated entry changed: %s %v", data, err)
	}
	assertSingleRebuildAttempts(t, dir, "none", "azure", "dotnet")
}
