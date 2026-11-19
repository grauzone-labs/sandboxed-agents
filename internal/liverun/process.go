package liverun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// exitEvidence records the suite's actual exit status, or a detected interrupted supervisor.
type exitEvidence struct {
	Code           int
	EndedAt        time.Time
	Interrupted    bool
	LogUnverified  bool
	PeakUnverified bool
	FailedCheck    string
}

// startedRun records only the owned supervisor identity and preparation duration.
type startedRun struct {
	Supervisor         processRef
	PreparationSeconds float64
	StartedAt          time.Time
	PreparationStart   time.Time
	PreparationEnd     time.Time
}

// runDirectory parses the sole argument for phases that consume a prepared run.
func runDirectory(args []string, diagnostics io.Writer) (string, error) {
	flags := flag.NewFlagSet("live-run", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("run-dir", "", "private run directory")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if *directory == "" || flags.NArg() != 0 {
		return "", fmt.Errorf("supply -run-dir")
	}
	return filepath.Abs(*directory)
}

// launch starts a fully detached supervisor and waits only for its startup evidence.
func (s *Service) launch(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	directory, err := runDirectory(args, diagnostics)
	if err != nil {
		return err
	}
	launchLock, err := os.OpenFile(filepath.Join(directory, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("private run directory unavailable")
	}
	defer launchLock.Close()
	if err = lockFile(launchLock); err != nil {
		return fmt.Errorf("this run is already starting")
	}
	if _, err = os.Stat(filepath.Join(directory, "started.json")); err == nil {
		return fmt.Errorf("run directory already used")
	}
	var options Options
	if err = readJSON(filepath.Join(directory, "options.json"), &options); err != nil {
		return fmt.Errorf("read prepared run options")
	}
	if err = options.normalizeConcurrency(); err != nil {
		return err
	}
	if err = s.checkCoordination(options); err != nil {
		return err
	}
	if options.RunDir != directory {
		return fmt.Errorf("prepared run directory mismatch")
	}
	if _, err = os.Stat(filepath.Join(directory, "launching.json")); err == nil {
		return fmt.Errorf("run directory already launched")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "_supervise", "-run-dir", directory)
	cmd.Dir = options.Checkout
	policy, err := s.sessionPolicy()
	if err == nil {
		err = policy.launchable()
	}
	if err != nil {
		return failCheck(options, "session-detachment", err)
	}
	detachCommand(cmd, policy)
	start := cmd.Start
	if s.StartSupervisor != nil {
		start = func() error { return s.StartSupervisor(cmd) }
	}
	if err = start(); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return failCheck(options, "session-detachment", err)
		}
		return fmt.Errorf("start detached supervisor: %w", err)
	}
	defer cmd.Process.Release()
	owner, err := ownedProcess(cmd.Process.Pid)
	if err == nil {
		err = writeJSON(filepath.Join(directory, "launching.json"), activeRun{Supervisor: owner, RunDir: directory})
	}
	if err != nil {
		return fmt.Errorf("startup ownership unverified; inspect status before retrying")
	}
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("startup wait interrupted; detached run may continue")
		case <-deadline.C:
			return fmt.Errorf("startup unverified; inspect status before retrying")
		case <-tick.C:
			var started startedRun
			if readJSON(filepath.Join(directory, "started.json"), &started) == nil {
				return json.NewEncoder(out).Encode(map[string]any{"started": true, "preparation_seconds": started.PreparationSeconds})
			}
			var ended exitEvidence
			if readJSON(filepath.Join(directory, "exit.json"), &ended) == nil {
				if ended.FailedCheck == "session-detachment" {
					return fmt.Errorf("check session-detachment failed; detached supervisor remained in a launching job or policy is unverified")
				}
				return fmt.Errorf("detached startup failed; private diagnostic retained")
			}
		}
	}
}

// supervise repeats preparation under the lock, runs the documented suite, and retains exit evidence.
func (s *Service) supervise(ctx context.Context, args []string) (result error) {
	directory, err := runDirectory(args, io.Discard)
	if err != nil {
		return err
	}
	var options Options
	if err = readJSON(filepath.Join(directory, "options.json"), &options); err != nil {
		return err
	}
	if err = options.normalizeConcurrency(); err != nil {
		return err
	}
	startedAt := options.PreparationStart
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	code := 1
	logUnverified := false
	peakUnverified := false
	var suiteEndedAt time.Time
	failedCheck := ""
	var engineLock *os.File
	activeFile := ""
	defer func() {
		if suiteEndedAt.IsZero() {
			suiteEndedAt = time.Now().UTC()
		}
		_, _, err := completion(directory, &exitEvidence{Code: code, EndedAt: suiteEndedAt, LogUnverified: logUnverified, PeakUnverified: peakUnverified, FailedCheck: failedCheck})
		if err != nil && result == nil {
			result = err
		}
		if activeFile != "" {
			_ = os.Remove(activeFile)
		}
		if engineLock != nil {
			_ = engineLock.Close()
		}
	}()
	policy, policyErr := s.sessionPolicy()
	if policyErr != nil || policy.InJob {
		failedCheck = "session-detachment"
		return failCheck(options, "session-detachment", fmt.Errorf("supervisor remains in inherited job or policy query failed: %v", policyErr))
	}
	snapshot, lock, err := s.prepareLocked(ctx, options)
	if err != nil {
		return err
	}
	engineLock = lock
	finishPeak := s.samplePeak(ctx, options, snapshot)
	defer func() { peakUnverified = !finishPeak() }()
	if _, err = os.Stat(filepath.Join(directory, "started.json")); err == nil {
		return fmt.Errorf("run directory already used")
	}
	if err = writeJSON(filepath.Join(directory, "before.json"), snapshot); err != nil {
		return err
	}
	supervisor, err := ownedProcess(os.Getpid())
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(directory, "suite.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if log.Close() != nil {
			logUnverified = true
		}
	}()
	active := activeRun{Supervisor: supervisor, RunDir: directory}
	if err = writeJSON(filepath.Join(directory, "processes.json"), active); err != nil {
		return err
	}
	if err = writeJSON(activePath(options, snapshot.EngineID), active); err != nil {
		return err
	}
	activeFile = activePath(options, snapshot.EngineID)

	buildDirectory, err := createBuildChannel(options, snapshot)
	if err != nil {
		buildDirectory = ""
		_ = os.WriteFile(filepath.Join(directory, "build-evidence-unverified"), []byte("trusted build channel unavailable\n"), 0600)
	}
	suite := exec.Command("go", "test", "./cmd/sandboxed-agents", "-run", "^TestLiveSuite$", "-timeout", "120m", "-v")
	suite.Dir = options.Checkout
	images := "0"
	if options.Images {
		images = "1"
	}
	suite.Env = hostEnvironment(s.Platform, map[string]string{
		"SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE": "1",
		"SANDBOXED_AGENTS_LIVE_OVERLAP":            strconv.Itoa(options.Overlap),
		"SANDBOXED_AGENTS_LIVE_BUILD_JOBS":         strconv.Itoa(options.BuildJobs),
		BuildEvidenceEnv:                           buildDirectory,
		"SANDBOXED_AGENTS_LIVE_TESTS":              "1", "SANDBOXED_AGENTS_GROUP": options.Group,
		"SANDBOXED_AGENTS_LIVE_RECORD_DIR": options.RecordDir, "SANDBOXED_AGENTS_LIVE_IMAGES_TESTS": images,
	})
	// Capture uses UTF-8 bytes directly; PowerShell's 5.1 text encoding is never involved.
	writer := &progressWriter{destination: log, filename: filepath.Join(directory, "progress.json")}
	activate, release, err := prepareSuite(suite, lock)
	if err != nil {
		return failCheck(options, "suite-supervision", err)
	}
	defer release()
	wait, err := capture.Start(suite, writer, writer)
	if err != nil {
		return failCheck(options, "suite-start", err)
	}
	waited := false
	defer func() {
		if !waited {
			release()
			_ = abortSuite(suite)
			_ = wait()
		}
	}()
	if err = activate(); err != nil {
		return failCheck(options, "suite-supervision", err)
	}
	active.Child, err = ownedProcess(suite.Process.Pid)
	if err != nil {
		active.Child = processRef{PID: suite.Process.Pid}
	}
	err = writeJSON(filepath.Join(directory, "processes.json"), active)
	if err == nil {
		err = writeJSON(activePath(options, snapshot.EngineID), active)
	}
	if err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(directory, "started.json"), startedRun{Supervisor: supervisor, PreparationSeconds: time.Since(startedAt).Seconds(), StartedAt: time.Now().UTC(), PreparationStart: startedAt, PreparationEnd: time.Now().UTC()}); err != nil {
		return err
	}
	err = wait()
	waited = true
	suiteEndedAt = time.Now().UTC()
	logUnverified = writer.logFailed || log.Sync() != nil
	if err == nil {
		code = 0
		return nil
	}
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
		if code < 0 {
			code = 137
		}
	} else {
		code = 1
	}
	return nil
}

// writeExitText retains a portable plain integer exit file for shell callers.
func writeExitText(directory string, code int) error {
	return os.WriteFile(filepath.Join(directory, "exit-status"), []byte(strconv.Itoa(code)+"\n"), 0600)
}

// completion serializes authoritative publication and interrupted recovery on a persistent per-run inode.
// It reuses existing JSON evidence and repairs the shell status from that authoritative value.
func completion(directory string, candidate *exitEvidence) (exitEvidence, bool, error) {
	var ended exitEvidence
	lock, err := os.OpenFile(filepath.Join(directory, "completion.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return ended, false, err
	}
	defer lock.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err = lockFile(lock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return ended, false, fmt.Errorf("completion publication busy or unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	err = readCompletion(filepath.Join(directory, "exit.json"), &ended)
	if err == nil {
		data, textErr := os.ReadFile(filepath.Join(directory, "exit-status"))
		if textErr == nil && strings.TrimSpace(string(data)) == strconv.Itoa(ended.Code) {
			return ended, true, nil
		}
		return ended, true, writeExitText(directory, ended.Code)
	}
	if !os.IsNotExist(err) {
		return ended, false, fmt.Errorf("existing completion unverified")
	}
	if candidate == nil {
		return ended, false, nil
	}
	ended = *candidate
	if err = writeExitText(directory, ended.Code); err != nil {
		return ended, false, err
	}
	err = writeJSON(filepath.Join(directory, "exit.json"), ended)
	return ended, err == nil, err
}

// readCompletion refuses malformed existing artifacts instead of inventing an authoritative successful exit.
func readCompletion(path string, ended *exitEvidence) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, ended) != nil || json.Unmarshal(data, &fields) != nil || ended.EndedAt.IsZero() {
		return fmt.Errorf("completion fields unverified")
	}
	for _, name := range []string{"Code", "EndedAt", "Interrupted"} {
		if fields[name] == nil {
			return fmt.Errorf("completion fields incomplete")
		}
	}
	return nil
}
