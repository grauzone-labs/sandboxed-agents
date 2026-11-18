package liverun

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// exitEvidence records the suite's actual exit status, or a detected interrupted supervisor.
type exitEvidence struct {
	Code        int
	EndedAt     time.Time
	Interrupted bool
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
	detachCommand(cmd)
	if err = cmd.Start(); err != nil {
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
	startedAt := options.PreparationStart
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	code := 1
	defer func() {
		if err := writeExitText(directory, code); err != nil && result == nil {
			result = err
		}
		if err := writeJSON(filepath.Join(directory, "exit.json"), exitEvidence{Code: code, EndedAt: time.Now().UTC()}); err != nil && result == nil {
			result = err
		}
	}()
	snapshot, lock, err := s.prepareLocked(ctx, options)
	if err != nil {
		return err
	}
	defer lock.Close()
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
	defer log.Close()
	active := activeRun{Supervisor: supervisor, RunDir: directory}
	if err = writeJSON(filepath.Join(directory, "processes.json"), active); err != nil {
		return err
	}
	if err = writeJSON(activePath(options, snapshot.EngineID), active); err != nil {
		return err
	}
	defer os.Remove(activePath(options, snapshot.EngineID))

	suite := exec.Command("go", "test", "./cmd/sandboxed-agents", "-run", "^TestLiveSuite$", "-timeout", "120m", "-v")
	suite.Dir = options.Checkout
	images := "0"
	if options.Images {
		images = "1"
	}
	suite.Env = hostEnvironment(s.Platform, map[string]string{
		"SANDBOXED_AGENTS_LIVE_STOP_AFTER_FAILURE": "1",
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
