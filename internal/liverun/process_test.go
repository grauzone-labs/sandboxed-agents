package liverun_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// TestMain exposes offline executable fixtures for the detached supervisor and fake host programs. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestMain(m *testing.M) {
	if os.Getenv("LIVERUN_FIXTURE") == "1" {
		role := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if role == "go" || role == "podman" || role == "git" || role == "ssh" {
			os.Exit(hostFixture(role, os.Args[1:]))
		}
		if len(os.Args) > 2 && os.Args[1] == "_fixture-producer" {
			directory := os.Args[2]
			asset := strings.Repeat("e", 64)
			err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(iid string) error {
				if err := os.WriteFile(filepath.Join(filepath.Dir(directory), "producer-ready"), []byte("producer started"), 0600); err != nil {
					return err
				}
				for {
					time.Sleep(time.Second)
				}
			}, func(string) (string, error) { return "", fmt.Errorf("unfinished producer was inspected") })
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			os.Exit(0)
		}
		if len(os.Args) > 1 && os.Args[1] == "_fixture-session" {
			command := exec.Command(os.Args[0], "run", "-run-dir", os.Args[2])
			if data, err := command.CombinedOutput(); err != nil {
				fmt.Fprintln(os.Stderr, err, string(data))
				os.Exit(2)
			}
			if err := os.WriteFile(filepath.Join(os.Getenv("LIVERUN_FIXTURE_ROOT"), "session-ready"), nil, 0600); err != nil {
				os.Exit(3)
			}
			for {
				time.Sleep(time.Second)
			}
		}
		service := liverun.New()
		service.CoordinationRoot = filepath.Join(os.Getenv("LIVERUN_FIXTURE_ROOT"), "locks")
		service.MachineDisk = func(string) (string, error) { return rootFixtureDisk(), nil }
		if value := os.Getenv("LIVERUN_FIXTURE_FREE_BYTES"); value != "" {
			service.FreeSpace = func(string) (uint64, error) { return strconv.ParseUint(value, 10, 64) }
		}
		if value := os.Getenv("LIVERUN_FIXTURE_SUITE_DEADLINE"); value != "" {
			deadline, err := time.ParseDuration(value)
			if err != nil {
				os.Exit(3)
			}
			service.SuiteDeadline = deadline
		}
		if len(os.Args) > 1 && os.Args[1] == "_supervise" && os.Getenv("LIVERUN_FIXTURE_INHERITED_JOB") == "1" {
			if os.Getenv("LIVERUN_FIXTURE_WAIT_LAUNCH") == "1" {
				deadline := time.Now().Add(5 * time.Second)
				directory := os.Args[len(os.Args)-1]
				for {
					if _, err := os.Stat(filepath.Join(directory, "launching.json")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						os.Exit(3)
					}
					time.Sleep(time.Millisecond)
				}
			}
			service.SessionProbe = func() (liverun.SessionPolicy, error) {
				return liverun.SessionPolicy{InJob: true, KillOnClose: true, Breakaway: true}, nil
			}
		}
		if err := service.RunDispatch(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// hostFixture answers host queries or runs a controlled UTF-8 suite without any real engine.
func hostFixture(role string, args []string) int {
	root := os.Getenv("LIVERUN_FIXTURE_ROOT")
	joined := strings.Join(args, " ")
	switch role {
	case "git":
		if args[0] == "rev-parse" {
			fmt.Println(strings.Repeat("a", 40))
		}
		return 0
	case "go":
		if args[0] == "env" {
			fmt.Println("go1.27.2")
			return 0
		}
		if args[0] == "test" {
			timeout := ""
			for index, arg := range args {
				if arg == "-timeout" && index+1 < len(args) {
					timeout = args[index+1]
				}
			}
			data, _ := json.Marshal(map[string]string{"suite_timeout": timeout, "overlap": os.Getenv(liverun.CaseOverlapEnv), "build_jobs": os.Getenv(liverun.BuildJobsEnv), "preflight_dir": os.Getenv(preflighttiming.DirEnv), "preflight_token": os.Getenv(preflighttiming.TokenEnv)})
			if err := os.WriteFile(filepath.Join(root, "suite-inputs.json"), data, 0600); err != nil {
				return 2
			}
			if os.Getenv("LIVERUN_FIXTURE_ORPHAN_INVOCATION") == "1" && orphanInvocation(root) != nil {
				return 2
			}
			fmt.Println("=== RUN   TestLiveSuite/images")
			fmt.Println("host_test.go:1: sandboxed-agents build private-image /private/path")
			fmt.Println("UTF-8 fixture: café")
			fmt.Println("    live_host_test.go:321: live operation Tool update: started")
			fmt.Println("private tool output /private/path")
			if err := os.WriteFile(filepath.Join(root, "suite-ready"), []byte("ready"), 0600); err != nil {
				return 2
			}
			for n := 0; n < 1000; n++ {
				if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
					fmt.Println("live operation cleanup: started")
					fmt.Println("suite final output")
					return 7
				}
				time.Sleep(10 * time.Millisecond)
			}
			return 3
		}
		return 4
	case "podman":
		switch {
		case strings.Contains(joined, "machine list"):
			fmt.Println(`[{"Name":"selected","Default":true,"VMType":"wsl"}]`)
		case strings.Contains(joined, "machine inspect"):
			_ = json.NewEncoder(os.Stdout).Encode([]any{map[string]any{"Name": "selected", "State": "running", "Rootful": false, "ConfigDir": map[string]string{"Path": root}, "SSHConfig": map[string]any{"IdentityPath": filepath.Join(root, "key"), "Port": 2222, "RemoteUsername": "user"}}})
		case strings.Contains(joined, "info --format"):
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"host": map[string]string{"hostname": "fixture-engine"}, "store": map[string]string{"graphRoot": "/"}})
		case strings.Contains(joined, "system df"):
			fmt.Println("TYPE TOTAL ACTIVE SIZE RECLAIMABLE")
		case strings.Contains(joined, "images --all"), strings.Contains(joined, "ps --all"):
			fmt.Println("[]")
		default:
			return 5
		}
		return 0
	case "ssh":
		free := os.Getenv("LIVERUN_FIXTURE_ENGINE_FREE_BYTES")
		if free == "" {
			free = "900"
		}
		fmt.Printf("Filesystem 1B-blocks Used Available Use%% Mounted on\n/dev/sda 1000 100 %s 10%% /\n", free)
		return 0
	}
	return 6
}

// orphanInvocation starts a tracked owned invocation that this fake suite never
// waits for, and names its group record in root/invocation once it exists.
// The suite's exit then leaves the invocation running in its own process group,
// as a test binary that dies mid-invocation does.
func orphanInvocation(root string) error {
	directory := os.Getenv(liverun.OwnedGroupsEnv)
	capture.TrackOwnedGroups(directory)
	go func() { _ = capture.RunOwned(context.Background(), exec.Command("sleep", "60"), nil, nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(directory); err == nil && len(entries) == 1 {
			return os.WriteFile(filepath.Join(root, "invocation"), []byte(entries[0].Name()), 0600)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("owned invocation was not recorded")
}

// fixtureSetup installs fake host programs beside an isolated checkout and prepares the real command.
func fixtureSetup(t *testing.T, additional ...string) (string, string, []string) {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { cleanupFixture(root) })
	t.Setenv("LIVERUN_FIXTURE", "1")
	t.Setenv("LIVERUN_FIXTURE_ROOT", root)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, role := range []string{"go", "git", "podman", "ssh"} {
		if err := installFixtureProgram(binary, filepath.Join(bin, role+suffix)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module fixture\n\ngo 1.27.2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	args := []string{"prepare", "-checkout", checkout, "-run-dir", private, "-record-dir", filepath.Join(root, "records"), "-revision", strings.Repeat("a", 40), "-allow-unbounded", "-unbounded-decision", "fixture invocation with no bound"}
	args = append(args, additional...)
	command := exec.Command(binary, args...)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare: %v\n%s", err, data)
	}
	return root, private, args
}

// installFixtureProgram hard-links the test executable to destination and copies
// it with executable permissions when the two paths are on different filesystems.
func installFixtureProgram(binary, destination string) error {
	if os.Link(binary, destination) == nil {
		return nil
	}
	source, err := os.Open(binary)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	return err
}

// waitFixture waits for observable process evidence rather than assuming a scheduling delay.
// Thirty seconds covers compound race-instrumented startup: run waits for
// repeated preparation through several fake process queries before readiness.
func waitFixture(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture evidence was not written: %s", path)
}

// TestRunSurvivesLauncherAndExcludesSecondSuite proves detachment, engine exclusion and retained real exit status at the command seam.
func TestRunSurvivesLauncherAndExcludesSecondSuite(t *testing.T) {
	root, private, args := fixtureSetup(t)
	binary, _ := os.Executable()
	launch := exec.Command(binary, "run", "-run-dir", private)
	data, err := launch.CombinedOutput()
	if err != nil {
		t.Fatalf("launch: %v\n%s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if launch.ProcessState == nil || !launch.ProcessState.Exited() {
		t.Fatal("launching process did not exit")
	}
	next := append([]string(nil), args...)
	for i := range next {
		if next[i] == private {
			next[i] = filepath.Join(root, "next")
		}
	}
	second := exec.Command(binary, next...)
	if data, err := second.CombinedOutput(); err == nil || !bytes.Contains(data, []byte("engine-exclusive")) {
		t.Fatalf("second suite admitted: %v %s", err, data)
	}
	// suite-ready proves the suite wrote its output, not that the supervisor drained it into progress.json.
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err = exec.Command(binary, "status", "-run-dir", private).CombinedOutput()
		if err != nil {
			t.Fatalf("status: %v %s", err, data)
		}
		for _, secret := range []string{"private-image", "/private/path", root} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("status leaked %q: %s", secret, data)
			}
		}
		if bytes.Contains(data, []byte("Tool update")) && bytes.Contains(data, []byte("images")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status missed progress: %s", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct{ Code int }
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 {
		t.Fatalf("exit evidence = %d; want fixture's 7", ended.Code)
	}
	if !bytes.Contains(mustReadFile(t, filepath.Join(private, "suite.log")), []byte("café")) {
		t.Fatal("log was not UTF-8")
	}
	// The supervisor publishes exit.json before its teardown closes the engine lock.
	deadline = time.Now().Add(30 * time.Second)
	for {
		data, err = exec.Command(binary, next...).CombinedOutput()
		if err == nil {
			break
		}
		if !bytes.Contains(data, []byte("engine-exclusive")) || time.Now().After(deadline) {
			t.Fatalf("engine was not released: %v %s", err, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestKilledRunDoesNotLeaveStaleEngineLock proves the prepare/run/status process-artifact seam excludes an orphan, admits its completed removal, and preserves prior sampled high water through a supervisor kill.
func TestKilledRunDoesNotLeaveStaleEngineLock(t *testing.T) {
	root, private, args := fixtureSetup(t)
	var before liverun.Snapshot
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "before.json")), &before); err != nil {
		t.Fatal(err)
	}
	samples := []liverun.PeakFilesystem{}
	for _, disk := range before.Disks {
		samples = append(samples, liverun.PeakFilesystem{Name: disk.Name, Path: disk.Path, StartFreeBytes: disk.FreeBytes, MinimumFreeBytes: disk.FreeBytes - 1, SampledGrowthBytes: 1, Samples: 2})
	}
	sum := sha256.Sum256([]byte(before.EngineID))
	observed := filepath.Join(root, "locks", fmt.Sprintf("%x-observed-peak.json", sum))
	cleanupArtifact(t, filepath.Dir(observed), filepath.Base(observed), liverun.PeakRecord{EngineID: before.EngineID, Revision: strings.Repeat("b", 40), SamplingComplete: true, Filesystems: samples})
	previous := mustReadFile(t, observed)
	binary, _ := os.Executable()
	data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	files, err := filepath.Glob(filepath.Join(root, "locks", "*-active.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("active record: %v %v", files, err)
	}
	var active struct{ Supervisor, Child struct{ PID int } }
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if json.Unmarshal(mustReadFile(t, files[0]), &active) == nil && active.Child.PID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active.Child.PID == 0 {
		t.Fatal("child identity not recorded")
	}
	supervisor, err := os.FindProcess(active.Supervisor.PID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(active.Child.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Kill(); _ = child.Kill() })
	if err = supervisor.Kill(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	next := append([]string(nil), args...)
	for i := range next {
		if next[i] == private {
			next[i] = filepath.Join(root, "next")
		}
	}
	data, err = exec.Command(binary, next...).CombinedOutput()
	if runtime.GOOS != "windows" && (err == nil || !bytes.Contains(data, []byte("engine-exclusive"))) {
		t.Fatalf("orphan child was not excluded: %v %s", err, data)
	}
	if err = child.Kill(); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err = exec.Command(binary, "status", "-run-dir", private).CombinedOutput()
		if err == nil && bytes.Contains(data, []byte(`"ended":true`)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || !bytes.Contains(data, []byte(`"exit_code":137`)) {
		t.Fatalf("missing interrupted exit evidence: %v %s", err, data)
	}
	if _, err = os.Stat(filepath.Join(private, "exit-status")); err != nil {
		t.Fatalf("plain exit file not retained: %v", err)
	}
	if !bytes.Equal(previous, mustReadFile(t, observed)) {
		t.Fatal("interrupted sampler erased prior useful observations")
	}
	data, err = exec.Command(binary, next...).CombinedOutput()
	if err != nil {
		t.Fatalf("dead run left stale exclusion: %v %s", err, data)
	}
}

// rootFixtureDisk names a fake machine filesystem without inspecting any real WSL installation.
func rootFixtureDisk() string { return os.Getenv("LIVERUN_FIXTURE_ROOT") }

// cleanupFixture stops only the processes recorded by this isolated offline fixture.
// It opens each process first and kills it only when the recorded creation
// identity still matches, so a reused PID is never stopped; an unverifiable
// process is left running.
func cleanupFixture(root string) {
	files, _ := filepath.Glob(filepath.Join(root, "locks", "*-active.json"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		type owned struct {
			PID      int
			Identity string
		}
		var active struct{ Supervisor, Child owned }
		if json.Unmarshal(data, &active) != nil {
			continue
		}
		for _, ref := range []owned{active.Child, active.Supervisor} {
			if ref.PID <= 0 || ref.Identity == "" {
				continue
			}
			// The process handle pins the opened process, so the identity check below cannot pass for a later reuse.
			process, err := os.FindProcess(ref.PID)
			if err != nil {
				continue
			}
			if liverun.OwnedProcessLive(ref.PID, ref.Identity) {
				_ = process.Kill()
			}
			_ = process.Release()
		}
	}
}

// TestRunRefusesConcurrentLaunchOfSameDirectory proves simultaneous launchers cannot overwrite the first run's exit evidence. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunRefusesConcurrentLaunchOfSameDirectory(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	binary, _ := os.Executable()
	first := exec.Command(binary, "run", "-run-dir", private)
	second := exec.Command(binary, "run", "-run-dir", private)
	var a, b bytes.Buffer
	first.Stdout = &a
	first.Stderr = &a
	second.Stdout = &b
	second.Stderr = &b
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	firstErr, secondErr := first.Wait(), second.Wait()
	successes := 0
	if firstErr == nil {
		successes++
	}
	if secondErr == nil {
		successes++
	}
	if successes != 1 {
		t.Fatalf("need one launch winner: %v %s; %v %s", firstErr, a.String(), secondErr, b.String())
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if _, err := os.Stat(filepath.Join(private, "exit.json")); !os.IsNotExist(err) {
		t.Fatal("losing launcher changed running suite's exit evidence")
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
}

// TestDuplicateSupervisorCannotPublishCompletion proves a second supervisor
// started for a running directory exits without writing exit evidence, so the
// owning supervisor still publishes the suite's real exit code. It exercises
// the run and _supervise commands and the owned exit artifact seam.
func TestDuplicateSupervisorCannotPublishCompletion(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if data, err := exec.Command(binary, "_supervise", "-run-dir", private).CombinedOutput(); err == nil {
		t.Fatalf("duplicate supervisor accepted: %s", data)
	}
	if _, err := os.Stat(filepath.Join(private, "exit.json")); !os.IsNotExist(err) {
		t.Fatalf("duplicate supervisor published completion: %s", mustReadFile(t, filepath.Join(private, "exit.json")))
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct{ Code int }
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 {
		t.Fatalf("exit evidence = %d; want the owning suite's 7", ended.Code)
	}
}

// TestRunReservesDirectoryBeforeStartingSupervisor proves run persists its
// launch reservation before it spawns the supervisor, and clears it when the
// spawn fails so no detached process can continue. It exercises the run
// command and the injected supervisor-start boundary seam.
func TestRunReservesDirectoryBeforeStartingSupervisor(t *testing.T) {
	svc, _, args, run := commandHarness(t)
	var out bytes.Buffer
	if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	reserved := false
	svc.StartSupervisor = func(*exec.Cmd) error {
		_, err := os.Stat(filepath.Join(run, "launching.json"))
		reserved = err == nil
		return fmt.Errorf("spawn refused")
	}
	if err := svc.RunDispatch(context.Background(), []string{"run", "-run-dir", run}, &out, &out); err == nil {
		t.Fatal("failed spawn reported a start")
	}
	if !reserved {
		t.Fatal("supervisor spawned before the run directory was reserved")
	}
	if _, err := os.Stat(filepath.Join(run, "launching.json")); !os.IsNotExist(err) {
		t.Fatalf("reservation kept after a spawn that started no process: %v", err)
	}
}

// TestPrepareRefusesClaimedRunDirectory proves prepare rewrites no prepared
// file while run holds launch.lock or after run reserved the directory, so the
// supervisor runs the options that run checked. It exercises the prepare
// command and the run directory's launch artifacts seam.
func TestPrepareRefusesClaimedRunDirectory(t *testing.T) {
	for _, claim := range []string{"lock", "reservation"} {
		t.Run(claim, func(t *testing.T) {
			svc, _, args, run := commandHarness(t)
			var out bytes.Buffer
			if err := svc.RunDispatch(context.Background(), args, &out, &out); err != nil {
				t.Fatal(err)
			}
			if claim == "lock" {
				lock, err := os.OpenFile(filepath.Join(run, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err = liverun.LockRunFile(lock); err != nil {
					t.Fatal(err)
				}
			} else {
				cleanupArtifact(t, run, "launching.json", map[string]any{"RunDir": run})
			}
			files := map[string][]byte{}
			for _, name := range []string{"options.json", "before.json", "preparation.json"} {
				files[name] = mustReadFile(t, filepath.Join(run, name))
			}
			// The repeated prepare saves a different plan, so any rewrite would show.
			if err := svc.RunDispatch(context.Background(), append(append([]string(nil), args...), "-images=false"), &out, &out); err == nil {
				t.Fatal("prepare rewrote a claimed run directory")
			}
			for name, data := range files {
				if !bytes.Equal(data, mustReadFile(t, filepath.Join(run, name))) {
					t.Fatalf("%s rewritten for a claimed run directory", name)
				}
			}
		})
	}
}

// TestRunAbortsSuitePastOuterDeadline proves the supervisor bounds the whole go
// test process, build included: a suite still running at the outer deadline is
// killed, its exit is retained as 137 with the suite-deadline check, and the
// engine is released. It exercises the run command and owned process artifacts seam.
func TestRunAbortsSuitePastOuterDeadline(t *testing.T) {
	root, private, args := fixtureSetup(t)
	t.Setenv("LIVERUN_FIXTURE_SUITE_DEADLINE", "1s")
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	// The fake suite never receives its release, so only the deadline can end it before its own 10 s exit.
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct {
		Code        int
		FailedCheck string
	}
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 137 || ended.FailedCheck != "suite-deadline" {
		t.Fatalf("deadline exit evidence = %+v; want 137 suite-deadline", ended)
	}
	next := append([]string(nil), args...)
	for i := range next {
		if next[i] == private {
			next[i] = filepath.Join(root, "next")
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := exec.Command(binary, next...).CombinedOutput()
		if err == nil {
			break
		}
		if !bytes.Contains(data, []byte("engine-exclusive")) || time.Now().After(deadline) {
			t.Fatalf("engine was not released after the deadline: %v %s", err, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunProgressPersistenceFailurePreservesSuiteExit proves a failed progress artifact cannot change the real suite outcome. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunProgressPersistenceFailurePreservesSuiteExit(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if err := os.Remove(filepath.Join(private, "progress.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(private, "progress.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct{ Code int }
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 {
		t.Fatalf("progress error changed actual suite code 7 to %d", ended.Code)
	}
	if !bytes.Contains(mustReadFile(t, filepath.Join(private, "suite.log")), []byte("suite final output")) {
		t.Fatal("suite output was not drained")
	}
}

// TestRunRecordsSampledPeakWithoutInventingBound proves the command retains run/filesystem provenance but fails closed on an unbounded last sample. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunRecordsSampledPeakWithoutInventingBound(t *testing.T) {
	root, private, args := fixtureSetup(t)
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	paths, err := filepath.Glob(filepath.Join(root, "locks", "*-observed-peak.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("last-run sampled peak absent: %v %v", paths, err)
	}
	var peak struct {
		Revision, EngineID, SampleContext string
		PeakAdditionalBytes               uint64
		Filesystems                       []struct {
			Name, Path string
			Samples    int
		}
	}
	if err = json.Unmarshal(mustReadFile(t, paths[0]), &peak); err != nil {
		t.Fatal(err)
	}
	if peak.Revision != strings.Repeat("a", 40) || peak.EngineID != "fixture-engine:/" || peak.SampleContext == "" || len(peak.Filesystems) == 0 || peak.Filesystems[0].Samples < 2 || peak.PeakAdditionalBytes != 0 {
		t.Fatalf("sample facts misrepresented: %+v", peak)
	}
	next := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "-allow-unbounded" {
			continue
		}
		if args[i] == "-unbounded-decision" {
			i++
			continue
		}
		if args[i] == private {
			next = append(next, filepath.Join(root, "next"))
		} else {
			next = append(next, args[i])
		}
	}
	if data, err := exec.Command(binary, next...).CombinedOutput(); err == nil || !bytes.Contains(data, []byte("no reliable bound")) {
		t.Fatalf("sample maximum accepted as bound: %v %s", err, data)
	}
}

// TestRunBuildChannelFailureIsSupplemental proves unavailable telemetry cannot prevent the suite or change its real exit code. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunBuildChannelFailureIsSupplemental(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	if err := os.Mkdir(filepath.Join(private, "build-evidence"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("telemetry prevented suite: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	var ended struct{ Code int }
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Code != 7 {
		t.Fatalf("telemetry changed actual suite code: %d", ended.Code)
	}
	if _, err := os.Stat(filepath.Join(private, "build-evidence-unverified")); err != nil {
		t.Fatal("evidence gap not retained")
	}
}

// TestRunPreservesAdmittedPeakBound proves the run command writes sampled observations separately and preserves the maintainer bound byte for byte. It exercises the prepare/run/status commands and owned process artifacts seam.
func TestRunPreservesAdmittedPeakBound(t *testing.T) {
	root, private, _ := fixtureSetup(t)
	sum := sha256.Sum256([]byte("fixture-engine:/"))
	bound := filepath.Join(root, "locks", fmt.Sprintf("%x-peak.json", sum))
	payload := []byte(`{"EngineID":"fixture-engine:/","PeakAdditionalBytes":1,"Basis":"reasoned fixture allowance at known byte boundary"}`)
	if err := os.WriteFile(bound, payload, 0600); err != nil {
		t.Fatal(err)
	}
	binary, _ := os.Executable()
	if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
		t.Fatalf("run: %v %s", err, data)
	}
	waitFixture(t, filepath.Join(root, "suite-ready"))
	if !bytes.Equal(mustReadFile(t, bound), payload) {
		t.Fatal("ongoing sampling destroyed admitted bound")
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFixture(t, filepath.Join(private, "exit.json"))
	if !bytes.Equal(mustReadFile(t, bound), payload) {
		t.Fatal("final sampling overwrote admitted bound")
	}
}

// TestKilledProducerDoesNotBlockIndependentCleanup proves the actual producer-process/artifact to Cleanup seam retains unknown images and reports orphan metadata after a kill.
func TestKilledProducerDoesNotBlockIndependentCleanup(t *testing.T) {
	for _, termination := range []string{"kill", "timeout"} {
		t.Run(termination, func(t *testing.T) {
			svc, fake, options := cleanupHarness(t)
			svc.BuildEvidence = nil
			directory := filepath.Join(options.RunDir, "build-evidence")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			invocation := strings.Repeat("f", 64)
			cleanupArtifact(t, directory, "manifest.json", map[string]any{"Version": 2, "Invocation": invocation, "Revision": strings.Repeat("a", 40), "EngineID": "engine:/private/storage"})
			binary, _ := os.Executable()
			ctx := context.Background()
			cancel := func() {}
			if termination == "timeout" {
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
			}
			defer cancel()
			producer := exec.CommandContext(ctx, binary, "_fixture-producer", directory)
			producer.Env = append(os.Environ(), "LIVERUN_FIXTURE=1")
			var output bytes.Buffer
			producer.Stdout, producer.Stderr = &output, &output
			if err := producer.Start(); err != nil {
				t.Fatal(err)
			}
			defer producer.Process.Kill()
			waitFixture(t, filepath.Join(options.RunDir, "producer-ready"))
			if termination == "kill" {
				if err := producer.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			if err := producer.Wait(); err == nil {
				t.Fatal("producer did not die")
			}
			intermediate := "sha256:" + strings.Repeat("b", 64)
			unknown := "sha256:" + strings.Repeat("d", 64)
			image := cleanupImageFixture(intermediate)
			image.RepoTags = nil
			image.Labels[legacyInvocationLabel] = strings.TrimPrefix(string(mustReadFile(t, filepath.Join(options.RunDir, "producer-ready"))), legacyInvocationLabel+"=")
			final := "sha256:" + strings.Repeat("a", 64)
			asset := strings.Repeat("e", 64)
			if err := liverun.CaptureSingleCommitBuild(directory, "localhost/sandboxed-agents:none-"+asset, asset, false, func(path string) error { return os.WriteFile(path, []byte(final), 0600) }, func(string) (string, error) { return final, nil }); err != nil {
				t.Fatal(err)
			}
			fake.images = map[string]cleanupFakeImage{intermediate: image, unknown: cleanupImageFixture(unknown), final: cleanupImageFixture(final)}
			var report bytes.Buffer
			err := svc.Cleanup(context.Background(), options, &report)
			if got := cleanupRemoved(fake); len(got) != 1 || got[0] != final {
				t.Fatalf("killed producer blocked independent proof: %v err=%v report=%s", got, err, &report)
			}
			if err == nil || !strings.Contains(report.String(), "incomplete or invalid") || !strings.Contains(report.String(), unknown) || !strings.Contains(report.String(), `"after"`) {
				t.Fatalf("orphan/unknown state lost: %v %s", err, &report)
			}
		})
	}
}

// TestSupervisorInheritedJobRefusesBeforeBuild proves direct supervisor and public run process/artifact seams publish a named startup failure without starting a suite.
func TestSupervisorInheritedJobRefusesBeforeBuild(t *testing.T) {
	for _, phase := range []string{"_supervise", "run"} {
		t.Run(phase, func(t *testing.T) {
			root, private, _ := fixtureSetup(t)
			t.Setenv("LIVERUN_FIXTURE_INHERITED_JOB", "1")
			if phase == "run" {
				t.Setenv("LIVERUN_FIXTURE_WAIT_LAUNCH", "1")
			}
			binary, _ := os.Executable()
			data, err := exec.Command(binary, phase, "-run-dir", private).CombinedOutput()
			if err == nil || !bytes.Contains(data, []byte("session-detachment")) {
				t.Fatalf("missing named job refusal: %v %s", err, data)
			}
			var ended struct {
				Code        int
				FailedCheck string
			}
			if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil {
				t.Fatal(err)
			}
			if ended.Code != 1 || ended.FailedCheck != "session-detachment" {
				t.Fatalf("wrong refusal evidence: %+v", ended)
			}
			for _, path := range []string{filepath.Join(private, "started.json"), filepath.Join(root, "suite-ready")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("unsafe supervisor reached suite/build", path)
				}
			}
		})
	}
}

// TestRunRechecksKnownSpaceDespiteUnboundedDecision proves the public run process seam repeats headroom guards before build on Linux and both native Windows filesystem roles.
func TestRunRechecksKnownSpaceDespiteUnboundedDecision(t *testing.T) {
	roles := []string{"host"}
	if runtime.GOOS == "windows" {
		roles = append(roles, "engine")
	}
	for _, role := range roles {
		for _, source := range []string{"bound", "observed"} {
			t.Run(role+"/"+source, func(t *testing.T) {
				root, private, _ := fixtureSetup(t)
				binary, _ := os.Executable()
				name, path := "engine", "/"
				if runtime.GOOS == "windows" {
					if role == "host" {
						name, path = "host", root
					}
					t.Setenv("LIVERUN_FIXTURE_ENGINE_FREE_BYTES", "900")
				}
				t.Setenv("LIVERUN_FIXTURE_FREE_BYTES", "100")
				if role == "engine" {
					t.Setenv("LIVERUN_FIXTURE_FREE_BYTES", "900")
					t.Setenv("LIVERUN_FIXTURE_ENGINE_FREE_BYTES", "100")
				}
				digest := sha256.Sum256([]byte("fixture-engine:/"))
				if source == "bound" {
					cleanupArtifact(t, filepath.Join(root, "locks"), fmt.Sprintf("%x-peak.json", digest), liverun.PeakRecord{EngineID: "fixture-engine:/", PeakAdditionalBytes: 100, Basis: "recorded fixture conservative bound"})
				} else {
					cleanupArtifact(t, filepath.Join(root, "locks"), fmt.Sprintf("%x-observed-peak.json", digest), liverun.PeakRecord{EngineID: "fixture-engine:/", Filesystems: []liverun.PeakFilesystem{{Name: name, Path: path, SampledGrowthBytes: 100, Samples: 2}}})
				}
				data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput()
				if err == nil {
					t.Fatalf("unbounded decision bypassed repeat headroom: %s", data)
				}
				if !bytes.Contains(mustReadFile(t, filepath.Join(private, "diagnostic.txt")), []byte("bytes")) {
					t.Fatal("headroom diagnosis absent")
				}
				for _, file := range []string{filepath.Join(root, "suite-ready"), filepath.Join(private, "started.json")} {
					if _, err := os.Stat(file); !os.IsNotExist(err) {
						t.Fatal("known insufficiency reached suite/build")
					}
				}
			})
		}
	}
}

// TestRunPreflightChannelIsSupplemental proves the detached command creates a
// fresh query-free private channel, clears ambient tokens, and preserves the
// suite's exit 7 even when channel creation fails, through the fake host seam.
// It also proves the command's actual timeout is the 120m measurement window.
func TestRunPreflightChannelIsSupplemental(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			root, private, _ := fixtureSetup(t)
			t.Setenv(preflighttiming.TokenEnv, "ambient-token")
			if blocked {
				if err := os.Mkdir(filepath.Join(private, "preflight-timing"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			binary, _ := os.Executable()
			if data, err := exec.Command(binary, "run", "-run-dir", private).CombinedOutput(); err != nil {
				t.Fatalf("start: %v %s", err, data)
			}
			waitFixture(t, filepath.Join(root, "suite-ready"))
			// Release before reporting assertions, preserving the fixture's owned cleanup.
			if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitFixture(t, filepath.Join(private, "exit.json"))
			var inputs map[string]string
			if err := json.Unmarshal(mustReadFile(t, filepath.Join(root, "suite-inputs.json")), &inputs); err != nil {
				t.Fatal(err)
			}
			if inputs["preflight_token"] != "" {
				t.Fatal("suite inherited a process token")
			}
			duration, err := time.ParseDuration(inputs["suite_timeout"])
			if err != nil || inputs["suite_timeout"] != "120m" || duration.Seconds() != preflighttiming.MaxSeconds {
				t.Fatalf("actual suite command timeout %q differs from measurement window %v: %v", inputs["suite_timeout"], preflighttiming.MaxSeconds, err)
			}
			if blocked {
				if inputs["preflight_dir"] != "" {
					t.Fatal("failed channel still enabled")
				}
				if _, err := os.Stat(filepath.Join(private, "preflight-timing-unverified")); err != nil {
					t.Fatal("observation loss unmarked")
				}
			} else {
				if inputs["preflight_dir"] != filepath.Join(private, "preflight-timing") {
					t.Fatalf("channel not passed: %v", inputs)
				}
				if _, err := preflighttiming.Read(inputs["preflight_dir"], strings.Repeat("a", 40)); err != nil {
					t.Fatal(err)
				}
			}
			var ended struct{ Code int }
			if err := json.Unmarshal(mustReadFile(t, filepath.Join(private, "exit.json")), &ended); err != nil || ended.Code != 7 {
				t.Fatalf("original exit changed: %+v %v", ended, err)
			}
		})
	}
}
