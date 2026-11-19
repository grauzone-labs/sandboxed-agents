package liverun_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/liverun"
)

// TestMain exposes offline executable fixtures for the detached supervisor and fake host programs.
func TestMain(m *testing.M) {
	if os.Getenv("LIVERUN_FIXTURE") == "1" {
		role := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if role == "go" || role == "podman" || role == "git" || role == "ssh" {
			os.Exit(hostFixture(role, os.Args[1:]))
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
		fmt.Println("Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 1000 100 900 10% /")
		return 0
	}
	return 6
}

// fixtureSetup installs fake host programs beside an isolated checkout and prepares the real command.
func fixtureSetup(t *testing.T) (string, string, []string) {
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
		if err := os.Link(binary, filepath.Join(bin, role+suffix)); err != nil {
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
	command := exec.Command(binary, args...)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare: %v\n%s", err, data)
	}
	return root, private, args
}

// waitFixture waits briefly for observable process evidence rather than assuming a scheduling delay.
func waitFixture(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
	status := exec.Command(binary, "status", "-run-dir", private)
	data, err = status.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v %s", err, data)
	}
	for _, secret := range []string{"private-image", "/private/path", root} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("status leaked %q: %s", secret, data)
		}
	}
	if !bytes.Contains(data, []byte("Tool update")) || !bytes.Contains(data, []byte("images")) {
		t.Fatalf("status missed progress: %s", data)
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
	second = exec.Command(binary, next...)
	if data, err := second.CombinedOutput(); err != nil {
		t.Fatalf("engine was not released: %v %s", err, data)
	}
}

// TestKilledRunDoesNotLeaveStaleEngineLock proves owned process liveness blocks an orphan then admits a new run after it dies.
func TestKilledRunDoesNotLeaveStaleEngineLock(t *testing.T) {
	root, private, args := fixtureSetup(t)
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
	data, err = exec.Command(binary, next...).CombinedOutput()
	if err != nil {
		t.Fatalf("dead run left stale exclusion: %v %s", err, data)
	}
}

// rootFixtureDisk names a fake machine filesystem without inspecting any real WSL installation.
func rootFixtureDisk() string { return os.Getenv("LIVERUN_FIXTURE_ROOT") }

// cleanupFixture stops only the processes recorded by this isolated offline fixture.
func cleanupFixture(root string) {
	files, _ := filepath.Glob(filepath.Join(root, "locks", "*-active.json"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var active struct{ Supervisor, Child struct{ PID int } }
		if json.Unmarshal(data, &active) != nil {
			continue
		}
		for _, pid := range []int{active.Child.PID, active.Supervisor.PID} {
			if pid > 0 {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
	}
}

// TestRunRefusesConcurrentLaunchOfSameDirectory proves simultaneous launchers cannot overwrite the first run's exit evidence.
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

// TestRunProgressPersistenceFailurePreservesSuiteExit proves a failed progress artifact cannot change the real suite outcome.
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

// TestRunRecordsSampledPeakWithoutInventingBound proves the command retains run/filesystem provenance but fails closed on an unbounded last sample.
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
	paths, err := filepath.Glob(filepath.Join(root, "locks", "*-peak.json"))
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

// TestRunBuildChannelFailureIsSupplemental proves unavailable telemetry cannot prevent the suite or change its real exit code.
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
