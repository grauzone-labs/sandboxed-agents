//go:build linux

package ownershipbench_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/ownershipbench"
)

const (
	// fakePodmanVariable makes this test binary act as the fake Podman engine the benchmark drives.
	fakePodmanVariable = "OWNERSHIP_BENCHMARK_TEST_FAKE_PODMAN"
	// benchmarkMainVariable makes this test binary run the benchmark command itself, for the signal test.
	benchmarkMainVariable = "OWNERSHIP_BENCHMARK_TEST_MAIN"
	image                 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	containerID           = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestMain runs the fake engine or the benchmark command when a re-execution asks for it, and
// otherwise the tests.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv(fakePodmanVariable) == "1":
		os.Exit(fakePodman(os.Args[1:]))
	case os.Getenv(benchmarkMainVariable) == "1":
		// The benchmark's engine processes are this binary again, as the fake engine.
		os.Unsetenv(benchmarkMainVariable)
		os.Setenv(fakePodmanVariable, "1")
		os.Exit(ownershipbench.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fakePodman answers the exact command sequence of the benchmark against files beside the private
// store, records every call in the BENCH_TEST_LOG file, and refuses an engine it should never see:
// a storage configuration outside the private store or inherited container configuration overrides.
func fakePodman(args []string) int {
	root := ""
	for i, arg := range args {
		if arg == "--root" && i+1 < len(args) {
			root = args[i+1]
		}
	}
	base := filepath.Dir(root)
	if configuration := os.Getenv("CONTAINERS_STORAGE_CONF"); !strings.HasPrefix(configuration, base+"/") {
		fmt.Fprintln(os.Stderr, "storage configuration outside the private store:", configuration)
		return 125
	}
	if os.Getenv("CONTAINERS_CONF") != "" || os.Getenv("CONTAINERS_CONF_OVERRIDE") != "" {
		fmt.Fprintln(os.Stderr, "inherited container configuration override")
		return 125
	}
	log, err := os.OpenFile(os.Getenv("BENCH_TEST_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 125
	}
	entry, _ := json.Marshal(args)
	log.Write(append(entry, '\n'))
	log.Close()
	command := []string{}
	for i, arg := range args {
		if arg == "--tmpdir" && i+2 <= len(args) {
			command = args[i+2:]
		}
	}
	label := func() string { data, _ := os.ReadFile(filepath.Join(base, "label")); return string(data) }
	head := func(n int) string { return strings.Join(command[:min(n, len(command))], " ") }
	switch {
	case head(1) == "--version":
		fmt.Println("podman version 5.7.1")
	case head(3) == "info --format json":
		fmt.Printf(`{"host":{"security":{"rootless":true},"kernel":"test"},"store":{"graphRoot":%q,"runRoot":%q,"graphDriverName":"overlay"}}`+"\n", root, filepath.Join(base, "run"))
	case head(2) == "images --all":
		if _, err := os.Stat(filepath.Join(base, "loaded")); err != nil {
			fmt.Println("[]")
		} else {
			fmt.Printf(`[{"Id":%q}]`+"\n", image)
		}
	case head(1) == "load":
		os.WriteFile(filepath.Join(base, "loaded"), nil, 0600)
		fmt.Println("Loaded image: fixture")
	case head(2) == "image inspect":
		fmt.Printf(`[{"Id":%q}]`+"\n", image)
	case head(2) == "volume create":
		_, value, _ := strings.Cut(command[3], "=")
		os.WriteFile(filepath.Join(base, "label"), []byte(value), 0600)
		fmt.Println(command[len(command)-1])
	case head(1) == "create":
		if marker := os.Getenv("BENCH_TEST_STARTED"); marker != "" {
			os.WriteFile(marker, []byte(fmt.Sprint(os.Getpid())), 0600)
		}
		if os.Getenv("BENCH_TEST_HANG") == "1" {
			time.Sleep(10 * time.Second)
		}
		fmt.Println(containerID)
	case head(2) == "start --attach":
		fmt.Println("identity/workspace:1000:1000")
	case head(2) == "container inspect":
		owner := label()
		if os.Getenv("BENCH_TEST_BAD_OWNER") == "1" {
			owner = "foreign"
		}
		fmt.Printf(`[{"Id":%q,"Config":{"Labels":{%q:%q}},"State":{"Running":false}}]`+"\n", containerID, ownershipbench.Label, owner)
	case head(2) == "volume inspect":
		fmt.Printf(`[{"Labels":{%q:%q}}]`+"\n", ownershipbench.Label, label())
	case head(1) == "rmi":
		os.Remove(filepath.Join(base, "loaded"))
	case head(1) == "rm", head(2) == "volume rm":
	default:
		fmt.Fprintln(os.Stderr, "unexpected", command)
		return 125
	}
	return 0
}

// fixture holds the files of one benchmark test: the fake engine on PATH, the archive, the
// baseline, and the call log.
type fixture struct {
	root, log string
	args      []string
}

// newFixture prepares the fake engine first on PATH with the benchmark's environment overrides set
// to unrelated files, which the fake refuses to see, and returns the standard argument list.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(bin, "podman")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.tar"), []byte("fixture archive"), 0600); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`{"host":{"security":{"rootless":true}},"store":{"graphRoot":%q,"runRoot":%q,"graphDriverName":"overlay"}}`, filepath.Join(root, "active"), filepath.Join(root, "active-run"))
	if err := os.WriteFile(filepath.Join(root, "info.json"), []byte(info), 0600); err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, log: filepath.Join(root, "calls.jsonl")}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakePodmanVariable, "1")
	t.Setenv("BENCH_TEST_LOG", f.log)
	t.Setenv("CONTAINERS_STORAGE_CONF", "/unrelated/storage.conf")
	t.Setenv("CONTAINERS_CONF", "/unrelated/containers.conf")
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "/unrelated/override.conf")
	for _, name := range []string{"BENCH_TEST_HANG", "BENCH_TEST_STARTED", "BENCH_TEST_BAD_OWNER"} {
		t.Setenv(name, "")
	}
	f.args = []string{"--archive", filepath.Join(root, "image.tar"), "--image", image, "--baseline-info", filepath.Join(root, "info.json"), "--parent", root, "--driver", "overlay", "--peak-bytes", "1024", "--timeout", "2"}
	return f
}

// set replaces the value that follows a flag in the argument list.
func (f *fixture) set(flag, value string) {
	for i, arg := range f.args {
		if arg == flag {
			f.args[i+1] = value
		}
	}
}

// run runs the benchmark in-process and returns its status and standard error.
func (f fixture) run(t *testing.T) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := ownershipbench.Main(f.args, &stdout, &stderr)
	return status, stderr.String()
}

// report reads the one report.json the run wrote below the parent.
func (f fixture) report(t *testing.T) (map[string]any, string) {
	t.Helper()
	reports, err := filepath.Glob(filepath.Join(f.root, "ownership-*", "report.json"))
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %v, error=%v; want one report", reports, err)
	}
	data, err := os.ReadFile(reports[0])
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report, filepath.Dir(reports[0])
}

// calls reads the fake engine's call log.
func (f fixture) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call []string
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

// assertNoCleanupCalls checks that no rm or rmi command reached the engine.
func assertNoCleanupCalls(t *testing.T, calls [][]string) {
	t.Helper()
	for _, call := range calls {
		for _, arg := range call {
			if arg == "rm" || arg == "rmi" {
				t.Fatalf("cleanup command sent: %v", call)
			}
		}
	}
}

// TestBenchmarkMeasuresCreateAndCleansOnlyOwnedObjects proves a passing run times the load and the keep-id create
// in a private local store, never prunes, resets, or forces, and removes only the proven container, workspace, and
// image, through the command against the fake engine.
func TestBenchmarkMeasuresCreateAndCleansOnlyOwnedObjects(t *testing.T) {
	f := newFixture(t)
	status, stderr := f.run(t)
	if status != 0 {
		t.Fatalf("status=%d stderr=%s", status, stderr)
	}
	report, directory := f.report(t)
	if report["result"] != "pass" {
		t.Fatalf("result = %v", report["result"])
	}
	elapsed := report["elapsed_seconds"].(map[string]any)
	for _, name := range []string{"load", "create"} {
		if _, ok := elapsed[name]; !ok {
			t.Fatalf("elapsed_seconds lacks %s: %v", name, elapsed)
		}
	}
	calls := f.calls(t)
	var create []string
	rmiSeen := false
	for _, call := range calls {
		if call[0] != "--remote=false" {
			t.Fatalf("call does not force the local engine: %v", call)
		}
		for i, arg := range call {
			if arg == "--root" && !strings.HasPrefix(call[i+1], directory+"/") {
				t.Fatalf("call uses a root outside the private store: %v", call)
			}
			if arg == "prune" || arg == "reset" || arg == "--force" {
				t.Fatalf("destructive engine argument: %v", call)
			}
		}
		if len(call) >= 3 && strings.Join(call[len(call)-3:], " ") == "rmi --no-prune "+image {
			rmiSeen = true
		}
		if contains(call, "create") && !contains(call, "volume") {
			create = call
		}
	}
	for _, required := range []string{"--userns=keep-id:uid=1000,gid=1000", "--pull=never", "--network=none"} {
		if !contains(create, required) {
			t.Fatalf("create lacks %s: %v", required, create)
		}
	}
	if !rmiSeen {
		t.Fatalf("image was not removed without pruning: %v", calls)
	}
}

// TestBenchmarkRefusesHeadroomWithoutCallingEngine proves a peak bound the free space does not exceed is refused
// before the engine runs or a store directory exists, through the command's validation.
func TestBenchmarkRefusesHeadroomWithoutCallingEngine(t *testing.T) {
	f := newFixture(t)
	f.set("--peak-bytes", "9223372036854775807")
	status, stderr := f.run(t)
	if status == 0 || !strings.Contains(stderr, "free space") {
		t.Fatalf("status=%d stderr=%s; want the free-space refusal", status, stderr)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatal("engine was called before the headroom refusal")
	}
	if stores, _ := filepath.Glob(filepath.Join(f.root, "ownership-*")); len(stores) != 0 {
		t.Fatalf("store directories created: %v", stores)
	}
}

// TestBenchmarkRefusesActiveStoreParent proves a parent below the baseline's graph root is refused before the engine
// runs, through the command's validation.
func TestBenchmarkRefusesActiveStoreParent(t *testing.T) {
	f := newFixture(t)
	active := filepath.Join(f.root, "active")
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	f.set("--parent", active)
	status, stderr := f.run(t)
	if status == 0 || !strings.Contains(stderr, "outside the active") {
		t.Fatalf("status=%d stderr=%s; want the active-store refusal", status, stderr)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatal("engine was called before the parent refusal")
	}
}

// TestBenchmarkTimeoutRetainsStateWithoutCleanupCalls proves a command that exceeds its deadline is killed, recorded
// as a pending cleanup, and followed by no removal, with the loaded image left in the store, through the command
// against a hanging fake engine.
func TestBenchmarkTimeoutRetainsStateWithoutCleanupCalls(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BENCH_TEST_HANG", "1")
	f.set("--timeout", "0.3")
	if status, _ := f.run(t); status == 0 {
		t.Fatal("hanging create passed")
	}
	report, directory := f.report(t)
	if !strings.Contains(fmt.Sprint(report["error"]), "create exceeded") || !strings.Contains(fmt.Sprint(report["cleanup"]), "pending") {
		t.Fatalf("report = %v; want the create deadline and a pending cleanup", report)
	}
	if elapsed := report["elapsed_seconds"].(map[string]any)["create"].(float64); elapsed >= 2 {
		t.Fatalf("create elapsed %v; want the deadline, not the hang", elapsed)
	}
	assertNoCleanupCalls(t, f.calls(t))
	if _, err := os.Stat(filepath.Join(directory, "loaded")); err != nil {
		t.Fatalf("loaded image marker removed: %v", err)
	}
}

// TestBenchmarkForeignContainerLabelPreventsCleanup proves a container whose label is not this run's nonce is retained
// with no removal command, through the command against a fake engine that reports a foreign owner.
func TestBenchmarkForeignContainerLabelPreventsCleanup(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BENCH_TEST_BAD_OWNER", "1")
	if status, _ := f.run(t); status == 0 {
		t.Fatal("foreign container passed")
	}
	report, _ := f.report(t)
	if !strings.Contains(fmt.Sprint(report["error"]), "ownership") {
		t.Fatalf("report error = %v; want the ownership refusal", report["error"])
	}
	assertNoCleanupCalls(t, f.calls(t))
}

// TestBenchmarkFailedProcessStartRetainsStoreWithoutCleanup proves an engine that cannot start leaves a failed report
// with a pending cleanup and the new store directory, with no engine call, through the command against an engine
// file that is not executable.
func TestBenchmarkFailedProcessStartRetainsStoreWithoutCleanup(t *testing.T) {
	f := newFixture(t)
	podman := filepath.Join(f.root, "bin", "podman")
	if err := os.Remove(podman); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(podman, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.run(t); status == 0 {
		t.Fatal("unstartable engine passed")
	}
	report, directory := f.report(t)
	if report["result"] != "fail" || !strings.Contains(fmt.Sprint(report["cleanup"]), "pending") {
		t.Fatalf("report = %v; want a failed result with a pending cleanup", report)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatal("engine call log exists although the engine could not start")
	}
	if info, err := os.Stat(filepath.Join(directory, "graph")); err != nil || !info.IsDir() {
		t.Fatalf("private graph root missing: %v", err)
	}
}

// TestBenchmarkSIGTERMStopsOwnedChildAndRetainsStore proves SIGTERM ends the running engine command through the owned
// process-group kill, writes a failed report with a pending cleanup, and sends no removal, through the command run as
// a process against a hanging fake engine.
func TestBenchmarkSIGTERMStopsOwnedChildAndRetainsStore(t *testing.T) {
	f := newFixture(t)
	marker := filepath.Join(f.root, "create-started")
	t.Setenv("BENCH_TEST_HANG", "1")
	t.Setenv("BENCH_TEST_STARTED", marker)
	t.Setenv(fakePodmanVariable, "")
	f.set("--timeout", "10")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, f.args...)
	command.Env = append(os.Environ(), benchmarkMainVariable+"=1")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	child := 0
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
		if child != 0 {
			syscall.Kill(-child, syscall.SIGKILL)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			fmt.Sscanf(string(data), "%d", &child)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("create did not start:\n%s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err := <-waited:
		if err == nil {
			t.Fatalf("benchmark passed after SIGTERM:\n%s", output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("benchmark did not end after SIGTERM:\n%s", output.String())
	}
	if _, err := os.Stat(filepath.Join("/proc", fmt.Sprint(child))); err == nil {
		t.Fatal("owned create child survived SIGTERM")
	}
	report, _ := f.report(t)
	if report["result"] != "fail" || !strings.Contains(fmt.Sprint(report["cleanup"]), "pending") {
		t.Fatalf("report = %v; want a failed result with a pending cleanup", report)
	}
	assertNoCleanupCalls(t, f.calls(t))
}

// contains tells whether a call holds an argument.
func contains(call []string, argument string) bool {
	for _, arg := range call {
		if arg == argument {
			return true
		}
	}
	return false
}
