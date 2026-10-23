package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// Restart policy of the service supervisor (ADR-0009, D1). No live run has measured these values.
var (
	// serviceRestartDelay is the first delay before a crashed service is started again; it doubles up to serviceRestartDelayLimit.
	serviceRestartDelay = time.Second
	// serviceRestartDelayLimit caps the delay between restarts.
	serviceRestartDelayLimit = 30 * time.Second
	// serviceHealthyAfter is how long a service must run before its start counts as healthy.
	serviceHealthyAfter = 60 * time.Second
	// serviceStartLimit is the number of consecutive unhealthy starts after which the supervisor gives up.
	serviceStartLimit = 5
	// serviceStopTimeout is how long a stop waits after SIGTERM before it kills the service's process group.
	serviceStopTimeout = 10 * time.Second
	// serviceStopGrace is the total wait of a stop request: the supervisor's stop timeout plus five seconds, after which it kills the service's process group and the supervisor.
	serviceStopGrace = serviceStopTimeout + 5*time.Second
	// serviceStartTimeout bounds how long a start waits for the supervisor to report the service as running or failed.
	serviceStartTimeout = 15 * time.Second
	// supervisorLockGrace is how long a starting supervisor retries past the shared probes of status readers before it yields to another supervisor.
	supervisorLockGrace = 2 * time.Second
)

// supervisorRecord is the supervisor's record, `state.json` in its directory. `running` counts only
// together with a held supervisor lock and a live process.
type supervisorRecord struct {
	State         string `json:"state"`
	Version       string `json:"version"`
	SupervisorPID int    `json:"supervisor_pid"`
	ServicePID    int    `json:"service_pid,omitempty"`
	Started       string `json:"started,omitempty"`
	LastExit      string `json:"last_exit,omitempty"`
	LastFailure   string `json:"last_failure,omitempty"`
	Restarts      int    `json:"restarts"`
}

// Kinds of supervised processes, named in the supervisor's arguments.
const (
	// supervisedService is the Tool service, `t3 serve` for T3 Code.
	supervisedService = "service"
	// supervisedTailnet is the Tool's tailnet node, `tailscaled` in userspace networking mode (ADR-0010).
	supervisedTailnet = "tailnet"
)

// supervised is one process a service supervisor keeps running for a Tool: where the supervisor keeps
// its lock, record, and logs, how the process is named in messages, and how it starts.
type supervised struct {
	// kind is supervisedService or supervisedTailnet.
	kind string
	// tool is the Tool's name.
	tool string
	// version is the installed version the Tool service runs on; empty for the tailnet node.
	version string
	// directory holds the supervisor lock, the state file, and the logs.
	directory string
	// label names the process in messages: `t3code service` or `t3code tailnet node`.
	label string
	// environment is appended to the supervisor's and the process's environment.
	environment []string
	// process is the program the supervisor spawns; its streams and process group are set by the supervisor.
	process Process
}

// toolService is the Tool service of the installed version: the launcher serving on the sandbox's loopback.
func toolService(directory, name, version string, definition catalog.Tool) supervised {
	return supervised{kind: supervisedService, tool: name, version: version, directory: serviceDirectory(directory), label: name + " service", environment: toolEnvironment(directory, definition),
		process: Process{Program: toolLauncher(directory, version, definition), Args: definition.ServiceArgs}}
}

// supervisorArgs are the manager arguments that run the supervisor of this process.
func (s supervised) supervisorArgs() []string {
	args := []string{"__supervise", s.tool, s.kind}
	if s.kind == supervisedService {
		args = append(args, s.version)
	}
	return args
}

// logPath is the current log of the supervised process.
func (s supervised) logPath() string {
	return supervisorLogPath(s.directory)
}

// serviceDirectory holds the supervisor lock, the state file, and the logs of a Tool service.
func serviceDirectory(directory string) string {
	return filepath.Join(directory, "service")
}

// supervisorLogPath is the current log inside a supervisor's directory.
func supervisorLogPath(supervisorDir string) string {
	return filepath.Join(supervisorDir, "service.log")
}

// readSupervisorRecord loads the supervisor's record from its directory, or an empty record when none exists.
func readSupervisorRecord(supervisorDir string) (supervisorRecord, error) {
	var state supervisorRecord
	data, err := os.ReadFile(filepath.Join(supervisorDir, "state.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("cannot read service state: %w", err)
	}
	return state, nil
}

// writeSupervisorRecord atomically replaces the supervisor's record in its directory.
func writeSupervisorRecord(supervisorDir string, state supervisorRecord) error {
	if err := os.MkdirAll(supervisorDir, 0700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(supervisorDir, "state.json"), ".state-", state)
}

// supervisorHeld reports whether a supervisor holds the lock in the directory; a missing directory has none.
func supervisorHeld(supervisorDir string) (bool, error) {
	if _, err := os.Stat(supervisorDir); os.IsNotExist(err) {
		return false, nil
	}
	return exclusivelyLocked(filepath.Join(supervisorDir, "supervisor.lock"))
}

// processAlive probes a process ID with signal zero.
func processAlive(effects Effects, pid int) bool {
	return pid > 0 && effects.Signal != nil && effects.Signal(pid, syscall.Signal(0)) == nil
}

// supervisedView is the derived view of a supervised process: its record, the reported state, and whether a supervisor holds the lock.
type supervisedView struct {
	state    supervisorRecord
	reported string
	held     bool
}

// describeSupervised derives the reported state of a supervised process from the lock, the record, and the live processes in the supervisor's directory.
func describeSupervised(supervisorDir string, effects Effects) (supervisedView, error) {
	state, err := readSupervisorRecord(supervisorDir)
	if err != nil {
		return supervisedView{}, err
	}
	held, err := supervisorHeld(supervisorDir)
	if err != nil {
		return supervisedView{}, err
	}
	view := supervisedView{state: state, held: held, reported: catalog.ServiceStopped}
	switch {
	case held && state.State == catalog.ServiceRunning && processAlive(effects, state.ServicePID):
		view.reported = catalog.ServiceRunning
	case held:
		// The supervisor is between starts or shutting down; the service is not running now.
	case state.State == catalog.ServiceFailed:
		view.reported = catalog.ServiceFailed
	}
	return view, nil
}

// fillServiceStatus adds the service state, the last failure, and the log path to a Tool status.
func fillServiceStatus(status *ToolStatus, directory string, effects Effects) error {
	state, failure, err := reportedState(serviceDirectory(directory), effects)
	if err != nil {
		return err
	}
	status.ServiceState, status.ServiceFailure, status.ServiceLog = state, failure, supervisorLogPath(serviceDirectory(directory))
	return nil
}

// reportedState is the state of a supervised process with its last failure, as status commands report them.
func reportedState(supervisorDir string, effects Effects) (state, failure string, err error) {
	view, err := describeSupervised(supervisorDir, effects)
	if err != nil {
		return "", "", err
	}
	switch {
	case view.reported == catalog.ServiceRunning:
	case view.held && view.state.LastFailure != "":
		failure = "supervisor is restarting the process; last: " + view.state.LastFailure
	default:
		failure = view.state.LastFailure
	}
	return view.reported, failure, nil
}

// startSupervised starts the supervisor of the process when none runs, and waits until a supervisor
// reports it as running on the expected version.
func startSupervised(s supervised, effects Effects) error {
	if effects.Detach == nil || effects.Now == nil || effects.Sleep == nil {
		return errors.New("service supervision is unavailable in this manager")
	}
	view, err := describeSupervised(s.directory, effects)
	if err != nil {
		return err
	}
	if view.held && view.state.Version != s.version {
		return fmt.Errorf("%s of version %s is still supervised; stop it before starting %s", s.label, view.state.Version, s.version)
	}
	if !view.held {
		// A stale record of an earlier supervisor must not be read as this start's outcome.
		if err := writeSupervisorRecord(s.directory, supervisorRecord{State: catalog.ServiceStopped, Version: s.version}); err != nil {
			return err
		}
		log, err := openSupervisorLog(s.directory)
		if err != nil {
			return err
		}
		err = effects.Detach(Process{Args: s.supervisorArgs(), Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment, Environment: s.environment, Stdout: log, Stderr: log})
		log.Close()
		if err != nil {
			return fmt.Errorf("%s supervisor could not start: %w", s.label, err)
		}
	}
	deadline := effects.Now().Add(serviceStartTimeout)
	for {
		view, err := describeSupervised(s.directory, effects)
		if err != nil {
			return err
		}
		if view.reported == catalog.ServiceRunning && view.state.Version == s.version {
			return nil
		}
		if view.reported == catalog.ServiceFailed && view.state.Version == s.version {
			return fmt.Errorf("%s failed to start: %s; log: %s", s.label, view.state.LastFailure, s.logPath())
		}
		if !effects.Now().Before(deadline) {
			return fmt.Errorf("%s did not report running within %gs; log: %s", s.label, serviceStartTimeout.Seconds(), s.logPath())
		}
		if err := effects.Sleep(context.Background(), 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// stopSupervised asks a running supervisor to end its process and waits until the lock is released.
func stopSupervised(s supervised, effects Effects) error {
	held, err := supervisorHeld(s.directory)
	if err != nil || !held {
		return err
	}
	state, err := readSupervisorRecord(s.directory)
	if err != nil {
		return err
	}
	if effects.Signal == nil || effects.Now == nil || effects.Sleep == nil {
		return errors.New("service supervision is unavailable in this manager")
	}
	if state.SupervisorPID > 0 {
		if err := effects.Signal(state.SupervisorPID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("cannot stop %s supervisor: %w", s.label, err)
		}
	}
	deadline := effects.Now().Add(serviceStopGrace)
	for {
		held, err := supervisorHeld(s.directory)
		if err != nil {
			return err
		}
		if !held {
			return nil
		}
		if !effects.Now().Before(deadline) {
			// The record is read again: a restarted service has a newer process ID than the one read above.
			current, err := readSupervisorRecord(s.directory)
			if err != nil {
				current = state
			}
			if current.ServicePID > 0 {
				effects.Signal(-current.ServicePID, syscall.SIGKILL)
			}
			if current.SupervisorPID > 0 {
				effects.Signal(current.SupervisorPID, syscall.SIGKILL)
			}
			// The kernel releases the lock of a killed supervisor; the service counts as stopped once it is free.
			for attempt := 0; attempt < 20; attempt++ {
				if held, err := supervisorHeld(s.directory); err == nil && !held {
					return nil
				}
				if err := effects.Sleep(context.Background(), 50*time.Millisecond); err != nil {
					return err
				}
			}
			return fmt.Errorf("%s did not stop within %gs and its processes could not be killed", s.label, serviceStopGrace.Seconds())
		}
		if err := effects.Sleep(context.Background(), 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// supervise is the detached service supervisor: it holds the supervisor lock of the named process,
// runs it as agent in its own process group, restarts it within the policy, and ends it on SIGTERM.
// Its arguments are `TOOL service VERSION` for the Tool service and `TOOL tailnet` for the tailnet node.
func supervise(args []string, stderr io.Writer, effects Effects) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: __supervise TOOL service VERSION | __supervise TOOL tailnet")
		return 2
	}
	name, kind := args[0], args[1]
	definition, err := catalog.Default.ResolveTool(name)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	version := ""
	switch {
	case kind == supervisedService && len(args) == 3 && catalog.ValidateVersion(args[2]) == nil:
		version = args[2]
	case kind == supervisedTailnet && len(args) == 2:
	default:
		fmt.Fprintln(stderr, "invalid supervisor request", strings.Join(args, " "))
		return 2
	}
	_, root, ok := verifiedAgentHome(effects, stderr)
	if !ok {
		return 1
	}
	if effects.Spawn == nil || effects.Signals == nil || effects.Now == nil || effects.Sleep == nil || effects.PID == nil {
		fmt.Fprintln(stderr, "service supervision is unavailable in this manager")
		return 1
	}
	directory := toolDirectory(root, name)
	s := toolService(directory, name, version, definition)
	if kind == supervisedTailnet {
		s = tailnetNode(directory, name)
	}
	if err := os.MkdirAll(s.directory, 0700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	release, held, err := acquireSupervisorLock(filepath.Join(s.directory, "supervisor.lock"), effects)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !held {
		// Another supervisor keeps the process; exactly one runs.
		return 0
	}
	defer release()
	// The supervisor's standard streams are the log, opened by the start that detached it.
	return exitStatus(runSupervisor(s, stderr, effects), stderr)
}

// acquireSupervisorLock takes the exclusive supervisor lock, retrying for a moment past the shared
// probes of status readers, and reports false only when another supervisor keeps the lock.
func acquireSupervisorLock(path string, effects Effects) (func(), bool, error) {
	deadline := effects.Now().Add(supervisorLockGrace)
	for {
		release, held, err := tryLock(path)
		if err != nil || held || !effects.Now().Before(deadline) {
			return release, held, err
		}
		if err := effects.Sleep(context.Background(), 20*time.Millisecond); err != nil {
			return nil, false, err
		}
	}
}

// openSupervisorLog keeps the previous run's log as `service.log.1` and opens a fresh `service.log` in the supervisor's directory.
func openSupervisorLog(supervisorDir string) (*os.File, error) {
	path := supervisorLogPath(supervisorDir)
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

// runSupervisor runs the supervised process until a termination signal or the restart bound ends it, recording every transition.
func runSupervisor(s supervised, log io.Writer, effects Effects) error {
	program := s.process.Program
	state := supervisorRecord{State: catalog.ServiceStopped, Version: s.version, SupervisorPID: effects.PID()}
	note := func(format string, args ...any) {
		fmt.Fprintf(log, "%s supervisor: %s\n", effects.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	record := func(reported string) error {
		state.State = reported
		return writeSupervisorRecord(s.directory, state)
	}
	signals := effects.Signals()
	delay, unhealthy := serviceRestartDelay, 0
	for {
		child, err := effects.Spawn(Process{Program: program, Args: s.process.Args, Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment, Environment: s.environment, ProcessGroup: true, Stdout: log, Stderr: log})
		started := effects.Now()
		if err != nil {
			state.LastFailure = fmt.Sprintf("cannot start %s: %v", program, err)
			note("%s", state.LastFailure)
			unhealthy++
		} else {
			state.ServicePID, state.Started, state.LastFailure = child.PID(), started.UTC().Format(time.RFC3339), ""
			if err := record(catalog.ServiceRunning); err != nil {
				child.Signal(syscall.SIGKILL)
				return err
			}
			note("started %s %s as pid %d", program, strings.Join(s.process.Args, " "), child.PID())
			exited := make(chan error, 1)
			go func() { exited <- child.Wait() }()
			select {
			case exit := <-exited:
				ran := effects.Now().Sub(started)
				state.LastExit = exitDescription(exit)
				state.LastFailure = fmt.Sprintf("service exited after %s: %s", ran.Round(time.Millisecond), state.LastExit)
				note("%s", state.LastFailure)
				if ran >= serviceHealthyAfter {
					unhealthy, delay = 0, serviceRestartDelay
				} else {
					unhealthy++
				}
			case <-signals:
				note("stopping on request")
				if err := endService(child, exited, effects); err != nil {
					note("%v", err)
				}
				state.ServicePID = 0
				return record(catalog.ServiceStopped)
			}
		}
		state.ServicePID = 0
		state.Restarts++
		if unhealthy >= serviceStartLimit {
			state.LastFailure = fmt.Sprintf("%s after %d consecutive failed starts; last: %s%s", catalog.ServiceFailed, unhealthy, state.LastFailure, logTail(s.logPath(), 5))
			note("%s", state.LastFailure)
			return record(catalog.ServiceFailed)
		}
		if err := record(catalog.ServiceStopped); err != nil {
			return err
		}
		note("restarting in %s", delay)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-signals:
				cancel()
			case <-ctx.Done():
			}
		}()
		err = effects.Sleep(ctx, delay)
		cancel()
		if err != nil {
			note("stopping on request while waiting to restart")
			return record(catalog.ServiceStopped)
		}
		delay = min(delay*2, serviceRestartDelayLimit)
	}
}

// logTail returns the last lines of the service log, read from its final 4 KiB, as one annotated string, or nothing when the log cannot be read.
func logTail(path string, lines int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := max(info.Size()-4096, 0)
	data := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(data, offset); err != nil && err != io.EOF {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	var kept []string
	for _, line := range all {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "; log tail: " + strings.Join(kept, " | ")
}

// endService sends SIGTERM to the launcher, which forwards it to the runtime, and kills the whole
// process group when the service has not exited within the stop timeout.
func endService(child Child, exited <-chan error, effects Effects) error {
	if err := child.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("cannot signal service: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timeout := make(chan struct{})
	go func() {
		if effects.Sleep(ctx, serviceStopTimeout) == nil {
			close(timeout)
		}
	}()
	select {
	case <-exited:
		return nil
	case <-timeout:
		child.Signal(syscall.SIGKILL)
		<-exited
		return fmt.Errorf("service did not exit within %gs after SIGTERM and was killed", serviceStopTimeout.Seconds())
	}
}

// exitDescription names a service exit from its wait error.
func exitDescription(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
