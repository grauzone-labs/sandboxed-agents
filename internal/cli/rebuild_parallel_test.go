package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// TestBuildJobsValidatesBeforePreflight proves positive build limits and the conservative default at the public parser and fake-engine CLI seams.
func TestBuildJobsValidatesBeforePreflight(t *testing.T) {
	invalid := []struct {
		name string
		args []string
	}{
		{"zero", []string{"build", "--jobs=0"}}, {"negative", []string{"build", "--jobs=-1"}},
		{"nonnumeric", []string{"build", "--jobs=no"}}, {"fraction", []string{"build", "--jobs=1.5"}},
		{"overflow", []string{"build", "--jobs=999999999999999999999999"}},
		{"empty", []string{"build", "--jobs="}}, {"missing", []string{"build", "--jobs"}},
		{"duplicate", []string{"build", "--jobs=1", "--jobs=2"}},
		{"up", []string{"up", "demo", "--jobs=2"}}, {"list", []string{"list", "--jobs=2"}},
		{"update-all", []string{"update", "--all", "--jobs=2"}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			f := linuxHost(t)
			status, output, calls := f.run(t, test.args...)
			if status != 2 || len(calls) != 0 || !strings.Contains(output, "--jobs") {
				t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
			}
		})
	}
	for _, test := range []struct {
		args []string
		jobs int
	}{
		{[]string{"build"}, 1}, {[]string{"build", "--jobs", "1"}, 1}, {[]string{"build", "--jobs=2"}, 2},
	} {
		request, err := cli.Parse(test.args, "default")
		if err != nil || request.BuildJobs != test.jobs {
			t.Fatalf("args=%v jobs=%d want=%d err=%v", test.args, request.BuildJobs, test.jobs, err)
		}
	}
}

// rebuildBuffer exposes captured CLI streams safely while build processes remain held.
type rebuildBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write appends process output without racing the test's live observations.
func (b *rebuildBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

// String snapshots the stream while the CLI may still be writing it.
func (b *rebuildBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

// rebuildFixture installs a build-specific fake ahead of all real executables on PATH.
func rebuildFixture(t *testing.T, sets ...string) (*fixture, string) {
	t.Helper()
	goProgram, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	f := linuxHost(t)
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	executable := filepath.Join(t.TempDir(), "podman"+suffix)
	command := exec.Command(goProgram, "build", "-o", executable, "./testdata/rebuild")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fake build: %v %s", err, output)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REBUILD_FAKE_DELEGATE", fakePath(f, "podman"))
	t.Setenv("REBUILD_FAKE_DIRECTORY", dir)
	var records []map[string]string
	for _, set := range sets {
		records = append(records, map[string]string{"Id": set + "-old"})
		f.responses["image inspect "+set+"-old"] = response{Output: `[{"Id":"` + set + `-old","Labels":{"io.sandboxed-agents.toolchains":"` + set + `"}}]`}
		releaseHandshakeFile(t, filepath.Join(dir, set+".tag"))
		if err := os.WriteFile(filepath.Join(dir, set+".tag"), []byte("old-"+set), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(records)
	f.responses["image ls *"] = response{Output: string(data)}
	f.responses["image inspect "+baseTag(t)] = response{Output: `[{"Id":"sha256:` + strings.Repeat("b", 64) + `"}]`}
	data, err = json.Marshal(f.responses)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PODMAN_RESPONSES", string(data))
	// Always free held fake children, including when an assertion aborts the test.
	t.Cleanup(func() {
		for _, name := range append([]string{"none"}, sets...) {
			_ = os.WriteFile(filepath.Join(dir, name+".release"), nil, 0600)
		}
	})
	return f, dir
}

// rebuildCompletion publishes one CLI exit status to any number of observers by closing a channel.
type rebuildCompletion struct {
	finished chan struct{}
	status   int
}

// wait returns the CLI exit status, failing the test when the CLI has not exited within the bound.
func (c *rebuildCompletion) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-c.finished:
		return c.status
	case <-time.After(10 * time.Second):
		t.Fatal("rebuild CLI did not exit")
		return 0
	}
}

// startRebuild runs the real CLI with safe live stream snapshots and returns its eventual exit status.
func startRebuild(t *testing.T, f *fixture, args ...string) (*rebuildBuffer, *rebuildBuffer, *rebuildCompletion) {
	t.Helper()
	stdout, stderr := &rebuildBuffer{}, &rebuildBuffer{}
	done := &rebuildCompletion{finished: make(chan struct{})}
	go func() {
		done.status = cli.RunWithHost(args, stdout, stderr, f.host)
		close(done.finished)
	}()
	t.Cleanup(func() {
		for _, name := range []string{"none", "azure", "dotnet", "native", "playwright"} {
			_ = os.WriteFile(filepath.Join(os.Getenv("REBUILD_FAKE_DIRECTORY"), name+".release"), nil, 0600)
		}
		select {
		case <-done.finished:
		case <-time.After(5 * time.Second):
			t.Error("held CLI did not exit after releases")
		}
	})
	return stdout, stderr, done
}

// TestBuildJobsBoundsToolchainsAfterBase proves base isolation, overlapping workers, failure continuation and the concurrency cap at the public CLI/fake Podman seam.
func TestBuildJobsBoundsToolchainsAfterBase(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet", "native")
	t.Setenv("REBUILD_FAKE_FAILURES", "azure")
	_, stderr, done := startRebuild(t, f, "build", "--jobs", "2")
	waitHandshakeFile(t, filepath.Join(dir, "none.started"))
	started, _ := filepath.Glob(filepath.Join(dir, "*.started"))
	if len(started) != 1 {
		t.Fatalf("toolchain started before base completed: %v", started)
	}
	releaseHandshakeFile(t, filepath.Join(dir, "none.release"))
	waitHandshakeFile(t, filepath.Join(dir, "azure.started"))
	waitHandshakeFile(t, filepath.Join(dir, "dotnet.started"))
	releaseHandshakeFile(t, filepath.Join(dir, "azure.release"))
	waitHandshakeFile(t, filepath.Join(dir, "native.started"))
	releaseHandshakeFile(t, filepath.Join(dir, "native.release"))
	releaseHandshakeFile(t, filepath.Join(dir, "dotnet.release"))
	status := done.wait(t)
	if status != 1 || !strings.Contains(stderr.String(), "failed toolchain sets: azure; existing sandboxes keep their image until update; list marks them as outdated") {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	active, peak := 0, 0
	for _, event := range strings.Fields(string(data)) {
		if event[0] == '+' {
			active++
			if active > peak {
				peak = active
			}
		} else {
			active--
		}
	}
	if active != 0 || peak != 2 || !strings.HasPrefix(string(data), "+none\n-none\n") {
		t.Fatalf("active=%d peak=%d events=%s", active, peak, data)
	}
	hash, hashErr := image.Hash(image.Context())
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	for _, name := range []string{"none", "azure", "dotnet", "native"} {
		checkRebuildArgv(t, dir, name, hash)
	}
	data, err = os.ReadFile(filepath.Join(dir, "azure.tag"))
	if err != nil || string(data) != "old-azure" {
		t.Fatalf("failed build replaced tag: %s %v", data, err)
	}
	for _, set := range []string{"dotnet", "native"} {
		data, err = os.ReadFile(filepath.Join(dir, set+".tag"))
		if err != nil || string(data) != "new-"+set {
			t.Fatalf("remaining build did not publish: %s %v", data, err)
		}
	}
}

// TestBuildJobsPublishesLiveProgressAndGroupedOutput proves real output is observed before held builds finish and each complete image block remains contiguous at the CLI stream/fake Podman seam.
func TestBuildJobsPublishesLiveProgressAndGroupedOutput(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet")
	releaseHandshakeFile(t, filepath.Join(dir, "none.release"))
	stdout, stderr, done := startRebuild(t, f, "build", "--jobs=2")
	waitHandshakeFile(t, filepath.Join(dir, "azure.started"))
	waitHandshakeFile(t, filepath.Join(dir, "dotnet.started"))
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(stderr.String(), "build progress azure:") || !strings.Contains(stderr.String(), "build progress dotnet:") {
		if time.Now().After(deadline) {
			t.Fatalf("no live progress before release: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		time.Sleep(time.Millisecond)
	}
	if strings.Contains(stdout.String(), "begin azure") || strings.Contains(stdout.String(), "begin dotnet") {
		t.Fatalf("partial image output escaped before completion: %s", stdout.String())
	}
	releaseHandshakeFile(t, filepath.Join(dir, "azure.release"))
	releaseHandshakeFile(t, filepath.Join(dir, "dotnet.release"))
	status := done.wait(t)
	if status != 0 {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	for _, name := range []string{"azure", "dotnet"} {
		// The complete stdout and stderr of one image must sit between that image's heading and result.
		block := stdout.String()
		start := strings.Index(block, "build output for toolchain set "+name+":\n")
		if start < 0 {
			t.Fatalf("missing block heading: %s", block)
		}
		block = block[start:]
		end := strings.Index(block, "built image localhost/sandboxed-agents:"+name+"-")
		if end < 0 {
			t.Fatalf("missing block result: %s", block)
		}
		block = block[:end]
		for _, piece := range []string{"begin " + name + "\n", "stderr " + name + "\n", "end " + name + "\n"} {
			if strings.Count(block, piece) != 1 {
				t.Errorf("missing original output %q in %s", piece, block)
			}
		}
		other := "dotnet"
		if name == "dotnet" {
			other = "azure"
		}
		if strings.Contains(block, "begin "+other) {
			t.Fatalf("interleaved blocks: %s", stdout.String())
		}
	}
}

// TestBuildJobsOneKeepsLiteralOutputAndArgv proves omitted and explicit jobs=1 preserve sorted serial output, flags, labels and guidance at the CLI/fake Podman seam.
func TestBuildJobsOneKeepsLiteralOutputAndArgv(t *testing.T) {
	for _, option := range []string{"", "--jobs=1"} {
		t.Run(option, func(t *testing.T) {
			f, dir := rebuildFixture(t, "native", "azure")
			for _, name := range []string{"none", "azure", "native"} {
				releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
			}
			args := []string{"build"}
			if option != "" {
				args = append(args, option)
			}
			stdout, stderr, done := startRebuild(t, f, args...)
			status := done.wait(t)
			hash, err := image.Hash(image.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := "begin none\nend none\nbuilt image " + image.Tag(hash, nil) + "\n" +
				"begin azure\nend azure\nbuilt image " + image.Tag(hash, []string{"azure"}) + "\n" +
				"begin native\nend native\nbuilt image " + image.Tag(hash, []string{"native"}) + "\n" +
				"existing sandboxes keep their image until update; list marks them as outdated\n"
			if status != 0 || stdout.String() != want || stderr.String() != "" {
				t.Fatalf("status=%d stdout=%q want=%q stderr=%q", status, stdout.String(), want, stderr.String())
			}
			checkRebuildArgv(t, dir, "none", hash)
			checkRebuildArgv(t, dir, "azure", hash)
			checkRebuildArgv(t, dir, "native", hash)
			events, err := os.ReadFile(filepath.Join(dir, "events"))
			if err != nil || string(events) != "+none\n-none\n+azure\n-azure\n+native\n-native\n" {
				t.Fatalf("serial order=%s err=%v", events, err)
			}
		})
	}
}

// checkRebuildArgv compares a complete fake-engine build invocation with literal refresh policy and image label expectations.
func checkRebuildArgv(t *testing.T, dir, name, hash string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name+".argv"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	var set []string
	if name != "none" {
		set = []string{name}
	}
	want := []string{"build", "--file", "CONTAINERFILE", "--tag", image.Tag(hash, set), "--label", "io.sandboxed-agents.image=sandboxed-agents", "--label", "io.sandboxed-agents.asset-sha256=" + hash, "--label", "io.sandboxed-agents.toolchains=" + name, "--no-cache"}
	if name == "none" {
		want = append(want, "--pull=always")
	} else {
		want = append(want, "--pull=never", "--build-arg", "BASE_IMAGE=sha256:"+strings.Repeat("b", 64), "--label", "io.sandboxed-agents.base-image=sha256:"+strings.Repeat("b", 64))
	}
	want = append(want, "CONTEXT")
	// The fake engine reads neither path, so the build file's place in the context is checked here.
	file := filepath.Join("toolchains", "Containerfile")
	if name == "none" {
		file = "Containerfile"
	}
	if len(args) > 2 && args[2] != filepath.Join(args[len(args)-1], file) {
		t.Fatalf("%s build file %q is not %s in context %q", name, args[2], file, args[len(args)-1])
	}
	if len(args) > 2 {
		args[2] = "CONTAINERFILE"
		args[len(args)-1] = "CONTEXT"
	}
	if !slices.Equal(args, want) {
		t.Fatalf("%s argv=%q want=%q", name, args, want)
	}
}

// TestBuildJobsShowsBaseProgressBeforeToolchains proves the opt-in output path reports a held base's actual output before release at the CLI/fake Podman seam.
func TestBuildJobsShowsBaseProgressBeforeToolchains(t *testing.T) {
	f, dir := rebuildFixture(t, "azure")
	stdout, stderr, done := startRebuild(t, f, "build", "--jobs=2")
	waitHandshakeFile(t, filepath.Join(dir, "none.started"))
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(stderr.String(), "build progress none:") {
		if time.Now().After(deadline) {
			t.Fatalf("no base progress before release: %s", stderr.String())
		}
		time.Sleep(time.Millisecond)
	}
	if stdout.String() != "" {
		t.Fatalf("partial base block escaped: %s", stdout.String())
	}
	releaseHandshakeFile(t, filepath.Join(dir, "none.release"))
	waitHandshakeFile(t, filepath.Join(dir, "azure.started"))
	releaseHandshakeFile(t, filepath.Join(dir, "azure.release"))
	status := done.wait(t)
	if status != 0 || !strings.Contains(stdout.String(), "build output for base image:\n") {
		t.Fatalf("status=%d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
}

// TestBuildJobsKeepsSortedFailuresAndCompleteLargeLogs proves concurrent failures retain full logs, aggregate names in canonical order, continue other sets and remove spools at the public CLI/fake Podman seam.
func TestBuildJobsKeepsSortedFailuresAndCompleteLargeLogs(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet", "native")
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Setenv("REBUILD_FAKE_FAILURES", "azure,native")
	t.Setenv("REBUILD_FAKE_PAYLOAD_BYTES", "5242880")
	for _, name := range []string{"none", "dotnet", "native"} {
		releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
	}
	stdout, stderr, done := startRebuild(t, f, "build", "--jobs=2")
	// azure remains held while dotnet and native finish, forcing reverse error completion order.
	waitHandshakeFile(t, filepath.Join(dir, "native.started"))
	waitRebuildOutput(t, stdout, "end native\n")
	releaseHandshakeFile(t, filepath.Join(dir, "azure.release"))
	status := done.wait(t)
	if status != 1 || !strings.Contains(stderr.String(), "failed toolchain sets: azure; native; existing sandboxes keep their image until update; list marks them as outdated") {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	if got := strings.Count(stdout.String(), "X"); got != 5242880*4 {
		t.Fatalf("large log truncated: retained %d payload bytes", got)
	}
	for _, name := range []string{"azure", "native"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".tag"))
		if err != nil || string(data) != "old-"+name {
			t.Fatalf("failed tag changed: %s %v", data, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(temp, "sandboxed-agents-build-output-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("spools retained: %v %v", files, err)
	}
	for _, call := range testutil.Invocations(t, f.fakes.PodmanLog) {
		if call[0] == "rmi" || (call[0] == "image" && len(call) > 1 && call[1] == "rm") {
			t.Fatalf("old image removal: %v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "sandboxed-agents", "locks", "ports")); !os.IsNotExist(err) {
		t.Fatalf("build took port-allocation lock: %v", err)
	}
}

// waitRebuildOutput waits for an actual captured output marker without releasing any held build.
func waitRebuildOutput(t *testing.T, output *rebuildBuffer, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), marker) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing output marker %q", marker)
}

// TestWindowsBuildJobsKeepsSelectedTarget proves parallel image work retains the checked Windows connection at the public CLI/fake Podman seam.
func TestWindowsBuildJobsKeepsSelectedTarget(t *testing.T) {
	f := windowsHost(t)
	f.responses["--connection chosen image ls *"] = response{Output: `[{"Id":"azure-old"}]`}
	f.responses["--connection chosen image inspect azure-old"] = response{Output: `[{"Id":"azure-old","Labels":{"io.sandboxed-agents.toolchains":"azure"}}]`}
	f.responses["--connection chosen image inspect "+baseTag(t)] = response{Output: `[{"Id":"base-new"}]`}
	f.responses["--connection chosen build *"] = response{}
	status, output, calls := f.run(t, "build", "--with", "native", "--jobs=2")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	builds := 0
	for _, call := range calls {
		if call[0] == "build" || call[0] == "image" {
			t.Fatalf("unbound image operation: %v", call)
		}
		if len(call) > 2 && call[2] == "build" {
			builds++
			if call[0] != "--connection" || call[1] != "chosen" {
				t.Fatalf("wrong target: %v", call)
			}
		}
	}
	if builds != 3 {
		t.Fatalf("build count=%d calls=%v", builds, calls)
	}
}

// TestBuildJobsBaseFailureStopsWorkers proves the opt-in capture path never starts a toolchain after a failed base at the public CLI/fake Podman seam.
func TestBuildJobsBaseFailureStopsWorkers(t *testing.T) {
	f := linuxHost(t)
	f.responses["image ls *"] = response{Output: `[]`}
	f.responses["build *"] = response{Exit: 125, Output: "base stdout failure", Stderr: "base stderr failure"}
	status, output, calls := f.run(t, "build", "--with", "native", "--jobs=2")
	if status != 1 || !strings.Contains(output, "base stdout failure") || !strings.Contains(output, "base stderr failure") {
		t.Fatalf("status=%d output=%s", status, output)
	}
	builds := 0
	for _, call := range calls {
		if call[0] == "build" {
			builds++
			if !slices.Contains(call, baseTag(t)) {
				t.Fatalf("toolchain started after failed base: %v", call)
			}
		}
	}
	if builds != 1 {
		t.Fatalf("builds=%d calls=%v", builds, calls)
	}
}

// rejectingBuildOutput refuses one output boundary while retaining all other writes.
type rejectingBuildOutput struct {
	buffer bytes.Buffer
	reject string
}

// Write returns a real caller-side I/O failure for the selected publication heading.
func (w *rejectingBuildOutput) Write(data []byte) (int, error) {
	if strings.Contains(string(data), w.reject) {
		return 0, errors.New("caller rejected build output")
	}
	return w.buffer.Write(data)
}

// String returns the successfully published output retained by the rejecting caller.
func (w *rejectingBuildOutput) String() string { return w.buffer.String() }

// TestBuildJobsPublicationFailureKeepsEngineOutcome proves output I/O failure does not rename a successfully rebuilt image as a failed set at the RunWithHost/fake Podman seam.
func TestBuildJobsPublicationFailureKeepsEngineOutcome(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet")
	for _, name := range []string{"none", "azure", "dotnet"} {
		releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
	}
	stdout := &rejectingBuildOutput{reject: "build output for toolchain set azure:"}
	var stderr bytes.Buffer
	status := cli.RunWithHost([]string{"build", "--jobs=2"}, stdout, &stderr, f.host)
	for _, name := range []string{"none", "azure", "dotnet"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".tag"))
		if err != nil || string(data) != "new-"+name {
			t.Fatalf("engine did not replace %s: %s %v", name, data, err)
		}
	}
	assertSingleRebuildAttempts(t, dir, "none", "azure", "dotnet")
	if status != 1 || strings.Contains(stderr.String(), "failed toolchain sets") || !strings.Contains(stderr.String(), "caller rejected build output") {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	if !strings.Contains(stderr.String(), "was rebuilt") || !strings.Contains(stdout.String(), "built image localhost/sandboxed-agents:azure-") {
		t.Fatalf("lost successful outcome: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

// TestBuildJobsBasePublicationFailureContinues proves a successfully rebuilt base's output error preserves remaining builds and guidance at the RunWithHost/fake Podman seam.
func TestBuildJobsBasePublicationFailureContinues(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet")
	for _, name := range []string{"none", "azure", "dotnet"} {
		releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
	}
	stdout := &rejectingBuildOutput{reject: "build output for base image:"}
	var stderr bytes.Buffer
	status := cli.RunWithHost([]string{"build", "--jobs=2"}, stdout, &stderr, f.host)
	for _, name := range []string{"none", "azure", "dotnet"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".tag"))
		if err != nil || string(data) != "new-"+name {
			t.Errorf("base output failure stopped %s: %s %v", name, data, err)
		}
	}
	assertSingleRebuildAttempts(t, dir, "none", "azure", "dotnet")
	if status != 1 || strings.Contains(stderr.String(), "failed toolchain sets") || !strings.Contains(stderr.String(), "was rebuilt") || !strings.Contains(stderr.String(), "existing sandboxes keep their image until update; list marks them as outdated") {
		t.Fatalf("status=%d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
}

// TestBuildJobsSeparatesMixedBuildAndOutputFailures proves a genuine failure alone enters the sorted set aggregation while another replaced tag's I/O error stays separate at RunWithHost/fake Podman.
func TestBuildJobsSeparatesMixedBuildAndOutputFailures(t *testing.T) {
	f, dir := rebuildFixture(t, "azure", "dotnet")
	t.Setenv("REBUILD_FAKE_FAILURES", "dotnet")
	for _, name := range []string{"none", "azure", "dotnet"} {
		releaseHandshakeFile(t, filepath.Join(dir, name+".release"))
	}
	stdout := &rejectingBuildOutput{reject: "build output for toolchain set azure:"}
	var stderr bytes.Buffer
	status := cli.RunWithHost([]string{"build", "--jobs=2"}, stdout, &stderr, f.host)
	if status != 1 || !strings.Contains(stderr.String(), "failed toolchain sets: dotnet;") || strings.Contains(stderr.String(), "failed toolchain sets: azure") || !strings.Contains(stderr.String(), "was rebuilt") || !strings.Contains(stderr.String(), "caller rejected build output") {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	for name, want := range map[string]string{"none": "new-none", "azure": "new-azure", "dotnet": "old-dotnet"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".tag"))
		if err != nil || string(data) != want {
			t.Fatalf("%s tag=%s want=%s err=%v", name, data, want, err)
		}
	}
	assertSingleRebuildAttempts(t, dir, "none", "azure", "dotnet")
}

// assertSingleRebuildAttempts proves no build was retried by checking the fake engine's complete interval records.
func assertSingleRebuildAttempts(t *testing.T, dir string, names ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Fields(string(data))
	if len(events) != 2*len(names) {
		t.Fatalf("unexpected build attempts: %s", data)
	}
	for _, name := range names {
		starts, ends := 0, 0
		for _, event := range events {
			if event == "+"+name {
				starts++
			}
			if event == "-"+name {
				ends++
			}
		}
		if starts != 1 || ends != 1 {
			t.Fatalf("%s attempts: %s", name, data)
		}
	}
}
