package manager_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/manager"
)

// fakeChild is a spawned service whose exit the test or a signal decides.
type fakeChild struct {
	pid    int
	exited chan struct{}
	once   sync.Once
	err    error
	// ignoreTerm makes the service survive SIGTERM, as a hung runtime would.
	ignoreTerm bool
}

// PID returns the fake process ID.
func (c *fakeChild) PID() int { return c.pid }

// Wait blocks until the fake service exits.
func (c *fakeChild) Wait() error { <-c.exited; return c.err }

// Signal ends the fake service: SIGTERM exits cleanly, SIGKILL exits killed, other signals are ignored.
func (c *fakeChild) Signal(sig os.Signal) error {
	switch sig {
	case syscall.SIGTERM:
		if !c.ignoreTerm {
			c.end(nil)
		}
	case syscall.SIGKILL:
		c.end(childExit(137))
	}
	return nil
}

// end records the exit once and releases Wait.
func (c *fakeChild) end(err error) {
	c.once.Do(func() { c.err = err; close(c.exited) })
}

// fakeServices fakes the processes and the clock behind the service supervisor for one test home.
type fakeServices struct {
	t           *testing.T
	home        string
	mu          sync.Mutex
	now         time.Time
	managerNow  time.Time
	nextPID     int
	supervisors map[int]chan os.Signal
	done        map[int]chan struct{}
	children    map[int]*fakeChild
	// spawnFailures is the number of service starts that fail before one succeeds.
	spawnFailures int
	// crashes is the number of services that exit at once after starting.
	crashes int
	// hang makes every spawned service ignore SIGTERM.
	hang       bool
	spawns     int
	spawnArgs  [][]string
	spawnEnv   [][]string
	detachArgs [][]string
}

// newFakeServices prepares the fake service system and stops every supervisor it started when the test ends.
func newFakeServices(t *testing.T, home string) *fakeServices {
	t.Helper()
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := &fakeServices{t: t, home: home, now: start, managerNow: start, nextPID: 4000, supervisors: map[int]chan os.Signal{}, done: map[int]chan struct{}{}, children: map[int]*fakeChild{}}
	t.Cleanup(func() { s.stopAll(t) })
	return s
}

// stopAll sends SIGTERM to every fake supervisor that still runs and waits until each has exited.
func (s *fakeServices) stopAll(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	var pending []chan struct{}
	for pid, signals := range s.supervisors {
		select {
		case <-s.done[pid]:
			continue
		default:
		}
		select {
		case signals <- syscall.SIGTERM:
		default:
		}
		pending = append(pending, s.done[pid])
	}
	s.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("a fake supervisor did not stop")
		}
	}
}

// clock returns the fake time of the supervisor, which the test and the supervisor's own waits advance.
func (s *fakeServices) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// advance moves the supervisor's fake clock forward, as time passing while a service runs.
func (s *fakeServices) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// sleep is the supervisor's wait: it advances the supervisor's clock by the duration and yields a
// little real time, so a stopped service is seen before a stop timeout fires.
func (s *fakeServices) sleep(ctx context.Context, d time.Duration) error {
	s.advance(d)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(min(d, 20*time.Millisecond)):
		return nil
	}
}

// managerClock returns the fake time of the manager that drives starts and stops; it is separate
// from the supervisor's clock, so the supervisor's restart delays do not consume the manager's deadlines.
func (s *fakeServices) managerClock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.managerNow
}

// managerSleep advances the manager's fake clock and yields a millisecond of real time, so polling loops see the supervisor goroutine progress.
func (s *fakeServices) managerSleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.managerNow = s.managerNow.Add(d)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Millisecond):
		return nil
	}
}

// signal delivers a signal to a fake supervisor or service by process ID, and probes liveness with signal zero.
func (s *fakeServices) signal(pid int, sig os.Signal) error {
	s.mu.Lock()
	signals, supervisor := s.supervisors[pid]
	done := s.done[pid]
	child := s.children[pid]
	if child == nil && pid < 0 {
		child = s.children[-pid]
	}
	s.mu.Unlock()
	if supervisor {
		select {
		case <-done:
			return syscall.ESRCH
		default:
		}
		if sig != syscall.Signal(0) {
			signals <- sig
		}
		return nil
	}
	if child != nil {
		select {
		case <-child.exited:
			return syscall.ESRCH
		default:
		}
		return child.Signal(sig)
	}
	return syscall.ESRCH
}

// spawn starts a fake service for the supervisor, failing or crashing as scripted.
func (s *fakeServices) spawn(p manager.Process) (manager.Child, error) {
	if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || !p.ProcessGroup {
		s.t.Errorf("service not spawned as agent in its own process group: %+v", p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spawns++
	s.spawnArgs = append(s.spawnArgs, append([]string{p.Program}, p.Args...))
	s.spawnEnv = append(s.spawnEnv, p.Environment)
	if s.spawnFailures > 0 {
		s.spawnFailures--
		return nil, fmt.Errorf("launcher missing")
	}
	s.nextPID++
	child := &fakeChild{pid: s.nextPID, exited: make(chan struct{}), ignoreTerm: s.hang}
	s.children[child.pid] = child
	if s.crashes > 0 {
		s.crashes--
		fmt.Fprintln(p.Stderr, "fatal: service crashed")
		child.end(childExit(1))
	}
	return child, nil
}

// detach runs the supervisor as a goroutine with its own fake process ID and signal channel.
func (s *fakeServices) detach(p manager.Process) error {
	if p.Identity != (manager.Identity{UID: 1000, GID: 1000}) || len(p.Args) != 3 || p.Args[0] != "__supervise" {
		s.t.Errorf("supervisor not detached as agent: %+v", p)
	}
	if p.Stdin != nil || p.Stdout == nil || p.Stderr == nil {
		s.t.Errorf("detached supervisor has standard input or lacks its log streams: %+v", p)
	}
	logFile, isFile := p.Stderr.(*os.File)
	if !isFile {
		s.t.Errorf("detached supervisor's log stream is not a file: %T", p.Stderr)
		return fmt.Errorf("log stream is not a file")
	}
	log, err := os.OpenFile(logFile.Name(), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		s.t.Errorf("cannot reopen the supervisor log: %v", err)
		return err
	}
	s.mu.Lock()
	s.nextPID++
	pid := s.nextPID
	signals := make(chan os.Signal, 4)
	done := make(chan struct{})
	s.supervisors[pid], s.done[pid] = signals, done
	s.detachArgs = append(s.detachArgs, p.Args)
	s.mu.Unlock()
	e := noProcesses(s.t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { return s.home, nil }
	e.Spawn, e.Signal, e.Now, e.Sleep = s.spawn, s.signal, s.clock, s.sleep
	e.PID = func() int { return pid }
	e.Signals = func() <-chan os.Signal { return signals }
	go func() {
		defer close(done)
		defer log.Close()
		if status := manager.RunWithEffects(p.Args, log, log, e); status != 0 {
			data, _ := os.ReadFile(log.Name())
			if !strings.Contains(string(data), "consecutive failed starts") {
				s.t.Errorf("supervisor %v exited with %d: %s", p.Args, status, data)
			}
		}
	}()
	return nil
}

// runningServices counts the fake services that have not exited.
func (s *fakeServices) runningServices() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, child := range s.children {
		select {
		case <-child.exited:
		default:
			count++
		}
	}
	return count
}

// wire adds the fake service system to the manager effects a test drives.
func (s *fakeServices) wire(e manager.Effects) manager.Effects {
	e.Detach, e.Signal, e.Now, e.Sleep = s.detach, s.signal, s.managerClock, s.managerSleep
	e.PID = func() int { return 1 }
	return e
}

// serviceEffects builds Tool effects with the fake service system for the test home.
func serviceEffects(t *testing.T, home string, registry toolRegistry, run *toolRun) (manager.Effects, *fakeServices) {
	t.Helper()
	e := toolEffects(t, home, registry, run)
	return e, run.services
}

// readServiceStateFile reads the supervisor's record from the test home.
func readServiceStateFile(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(toolDirectory(home), "service", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestToolsEnableStartsOneDetachedServiceAsAgent proves a first enable detaches one supervisor as agent, which spawns `t3 serve` on loopback as agent in its own process group with the Tool's home, reports the service as running, and that repeated enable and restore never start a second one at RunWithEffects with a real supervisor lock.
func TestToolsEnableStartsOneDetachedServiceAsAgent(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	if out := runTools(t, e, 0, "enable", "t3code"); !strings.Contains(out, "version 0.1.2 (channel latest); service running") {
		t.Fatalf("enable output=%s", out)
	}
	launcher := filepath.Join(toolDirectory(home), "npm", "0.1.2", "bin", "t3")
	want := []string{launcher, "serve", "--host", "127.0.0.1", "--port", "3773"}
	if services.spawns != 1 || !slices.Equal(services.spawnArgs[0], want) || !slices.Contains(services.spawnEnv[0], "T3CODE_HOME="+filepath.Join(toolDirectory(home), "home")) {
		t.Fatalf("service spawn=%v env=%v", services.spawnArgs, services.spawnEnv)
	}
	if !slices.Equal(services.detachArgs[0], []string{"__supervise", "t3code", "0.1.2"}) {
		t.Fatalf("supervisor args=%v", services.detachArgs)
	}
	status := readToolStatus(t, e)
	if status.ServiceState != "running" || status.ServiceLog != filepath.Join(toolDirectory(home), "service", "service.log") || status.ServiceFailure != "" {
		t.Fatalf("status=%+v", status)
	}
	for _, args := range [][]string{{"enable", "t3code"}, {"restore"}, {"enable", "t3code"}} {
		runTools(t, e, 0, args...)
	}
	if services.spawns != 1 || len(services.detachArgs) != 1 || services.runningServices() != 1 {
		t.Fatalf("repeated starts changed the service: spawns=%d supervisors=%d running=%d", services.spawns, len(services.detachArgs), services.runningServices())
	}
	state := readServiceStateFile(t, home)
	if state["state"] != "running" || state["version"] != "0.1.2" || state["supervisor_pid"].(float64) <= 0 {
		t.Fatalf("state=%v", state)
	}
	if _, err := os.Stat(filepath.Join(toolDirectory(home), "service", "service.log")); err != nil {
		t.Fatalf("service log missing: %v", err)
	}
}

// TestToolsSwitchRestartsOnTheNewVersionOrReinstatesTheOld proves a version switch stops the running service, starts it on the new version, and commits the selection only then, while a new version whose service does not start leaves the old version installed, selected, and running at RunWithEffects with a real supervisor lock.
func TestToolsSwitchRestartsOnTheNewVersionOrReinstatesTheOld(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	registry := toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2", "0.2.0": "0.2.0", "0.3.0": "0.3.0"}
	e, services := serviceEffects(t, home, registry, run)
	runTools(t, e, 0, "enable", "t3code")
	before, link := readToolStatus(t, e), commandLink(t, home)
	services.mu.Lock()
	services.spawnFailures = 5
	services.mu.Unlock()
	out := runTools(t, e, 1, "enable", "t3code", "--version", "0.2.0")
	if !strings.Contains(out, "t3code service failed to start") || !strings.Contains(out, "the previous version 0.1.2 is running again") {
		t.Fatalf("failed switch output=%s", out)
	}
	if got := readToolStatus(t, e); got != before || commandLink(t, home) != link {
		t.Fatalf("failed switch changed the selection: %+v link=%q", got, commandLink(t, home))
	}
	if services.runningServices() != 1 || !strings.HasSuffix(services.spawnArgs[len(services.spawnArgs)-1][0], filepath.Join("npm", "0.1.2", "bin", "t3")) {
		t.Fatalf("old version not running again: running=%d spawns=%v", services.runningServices(), services.spawnArgs)
	}
	prefixes, _ := toolVersions(t, home)
	if !slices.Equal(prefixes, []string{"0.1.2"}) {
		t.Fatalf("failed switch kept the new prefix: %q", prefixes)
	}
	if out := runTools(t, e, 0, "update", "t3code", "--version", "0.3.0"); !strings.Contains(out, "version 0.3.0 (pin 0.3.0); service running") {
		t.Fatalf("switch output=%s", out)
	}
	if got := readToolStatus(t, e); got.InstalledVersion != "0.3.0" || got.Pin != "0.3.0" || got.ServiceState != "running" {
		t.Fatalf("switched status=%+v", got)
	}
	last := services.spawnArgs[len(services.spawnArgs)-1][0]
	if services.runningServices() != 1 || !strings.HasSuffix(last, filepath.Join("npm", "0.3.0", "bin", "t3")) {
		t.Fatalf("service not on the new version: running=%d last=%s", services.runningServices(), last)
	}
	state := readServiceStateFile(t, home)
	if state["version"] != "0.3.0" {
		t.Fatalf("state=%v", state)
	}
}

// TestSupervisorRestartsCrashesWithinTheBoundAndReportsFailure proves the supervisor restarts a crashing service with growing delays, gives up after five consecutive failed starts, which leaves a first enable not enabled and a restore reporting the recorded failure, counts a long run as healthy, and keeps the previous log, at RunWithEffects with a fake clock.
func TestSupervisorRestartsCrashesWithinTheBoundAndReportsFailure(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	services.mu.Lock()
	services.crashes = 5
	services.mu.Unlock()
	if out := runTools(t, e, 1, "enable", "t3code"); !strings.Contains(out, "t3code service failed to start") || !strings.Contains(out, "consecutive failed starts") {
		t.Fatalf("enable with a crashing service output=%s", out)
	}
	if services.spawns != 5 {
		t.Fatalf("spawns=%d want 5 before giving up", services.spawns)
	}
	if got := readToolStatus(t, e); got.Enabled {
		t.Fatalf("a first enable whose service never ran enabled the Tool: %+v", got)
	}
	if prefixes, _ := toolVersions(t, home); len(prefixes) != 0 {
		t.Fatalf("failed first enable kept prefixes %q", prefixes)
	}
	log, err := os.ReadFile(filepath.Join(toolDirectory(home), "service", "service.log"))
	if err != nil || !strings.Contains(string(log), "fatal: service crashed") || !strings.Contains(string(log), "restarting in 1s") || !strings.Contains(string(log), "restarting in 8s") {
		t.Fatalf("service log=%s error=%v", log, err)
	}
	runTools(t, e, 0, "enable", "t3code")
	stopFakeSupervisors(t, services)
	services.mu.Lock()
	services.crashes = 5
	services.mu.Unlock()
	if out := runTools(t, e, 1, "restore"); !strings.Contains(out, "consecutive failed starts") {
		t.Fatalf("restore with a crashing service output=%s", out)
	}
	status := readToolStatus(t, e)
	if status.ServiceState != "failed" || !strings.Contains(status.ServiceFailure, "5 consecutive failed starts") || status.InstalledVersion != "0.1.2" {
		t.Fatalf("status after give-up=%+v", status)
	}
	if out := runTools(t, e, 0, "restore"); !strings.Contains(out, "t3code service running") {
		t.Fatalf("restore output=%s", out)
	}
	if got := readToolStatus(t, e); got.ServiceState != "running" || got.ServiceFailure != "" {
		t.Fatalf("restored status=%+v", got)
	}
	// One crash after a healthy hour restarts without counting toward the bound.
	services.advance(time.Hour)
	services.mu.Lock()
	for _, child := range services.children {
		select {
		case <-child.exited:
		default:
			child.end(childExit(2))
		}
	}
	services.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for services.runningServices() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if services.runningServices() != 1 {
		t.Fatalf("service not restarted after a healthy run: running=%d", services.runningServices())
	}
	if _, err := os.Stat(filepath.Join(toolDirectory(home), "service", "service.log.1")); err != nil {
		t.Fatalf("previous log not kept: %v", err)
	}
}

// stopFakeSupervisors sends SIGTERM to every fake supervisor and waits until each has exited.
func stopFakeSupervisors(t *testing.T, services *fakeServices) {
	t.Helper()
	services.stopAll(t)
}

// TestToolsSwitchKillsAServiceThatIgnoresSIGTERM proves a version switch whose old service ignores SIGTERM ends it through the supervisor's SIGKILL after the stop timeout, leaves exactly one service on the new version, and that a restore refused under the manager lock changes nothing, at RunWithEffects with a fake clock.
func TestToolsSwitchKillsAServiceThatIgnoresSIGTERM(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2", "0.2.0": "0.2.0"}, run)
	services.mu.Lock()
	services.hang = true
	services.mu.Unlock()
	runTools(t, e, 0, "enable", "t3code")
	if out := runTools(t, e, 0, "update", "t3code", "--version", "0.2.0"); !strings.Contains(out, "version 0.2.0 (pin 0.2.0); service running") {
		t.Fatalf("switch output=%s", out)
	}
	if services.runningServices() != 1 {
		t.Fatalf("services running after the switch=%d", services.runningServices())
	}
	last := services.spawnArgs[len(services.spawnArgs)-1][0]
	if !strings.HasSuffix(last, filepath.Join("npm", "0.2.0", "bin", "t3")) {
		t.Fatalf("running service is not the new version: %s", last)
	}
	log, err := os.ReadFile(filepath.Join(toolDirectory(home), "service", "service.log.1"))
	if err != nil || !strings.Contains(string(log), "did not exit within 10s after SIGTERM and was killed") {
		t.Fatalf("previous log=%s error=%v", log, err)
	}
	// A restore while another change holds the manager lock is refused at once and starts nothing.
	unlock, locked, err := lockFor(t, home)
	if err != nil || !locked {
		t.Fatalf("cannot take the manager lock: %v", err)
	}
	spawns := services.spawns
	out := runTools(t, e, 1, "restore")
	unlock()
	if !strings.Contains(out, "another agent or tool change is in progress; retry") || services.spawns != spawns {
		t.Fatalf("restore under a held lock: %s spawns=%d", out, services.spawns-spawns)
	}
}

// TestToolsStatusReportsStoppedWithoutASupervisor proves a stale running record without a supervisor, as a container stop leaves it, reports stopped, and restore starts the service again at RunWithEffects.
func TestToolsStatusReportsStoppedWithoutASupervisor(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	runTools(t, e, 0, "enable", "t3code")
	stopFakeSupervisors(t, services)
	state := readServiceStateFile(t, home)
	state["state"] = "running"
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(toolDirectory(home), "service", "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := readToolStatus(t, e); got.ServiceState != "stopped" {
		t.Fatalf("stale record reported as %+v", got)
	}
	if out := runTools(t, e, 0, "restore"); !strings.Contains(out, "t3code service running") {
		t.Fatalf("restore output=%s", out)
	}
	if got := readToolStatus(t, e); got.ServiceState != "running" || services.runningServices() != 1 {
		t.Fatalf("restored status=%+v running=%d", got, services.runningServices())
	}
	var statuses []manager.ToolStatus
	if err := json.Unmarshal([]byte(runTools(t, e, 0, "list")), &statuses); err != nil || len(statuses) != 1 || statuses[0].ServiceState != "running" || statuses[0].Name != "t3code" {
		t.Fatalf("list=%+v error=%v", statuses, err)
	}
}

// TestToolsListAndRestoreSkipDisabledTools proves list returns an empty array and restore starts nothing on a sandbox without an enabled Tool at RunWithEffects.
func TestToolsListAndRestoreSkipDisabledTools(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{}, run)
	if out := runTools(t, e, 0, "list"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("list=%s", out)
	}
	if out := runTools(t, e, 0, "restore"); out != "" || len(services.detachArgs) != 0 {
		t.Fatalf("restore output=%q detached=%v", out, services.detachArgs)
	}
	if got := readToolStatus(t, e); got.Enabled || got.ServiceState != "" {
		t.Fatalf("disabled status=%+v", got)
	}
}

// TestSupervisorRequestsValidateBeforeHome proves the supervisor entry point rejects unknown tools, invalid versions, and other identities before reading home at RunWithEffects.
func TestSupervisorRequestsValidateBeforeHome(t *testing.T) {
	e := noProcesses(t, manager.Identity{UID: 1000, GID: 1000})
	e.HomeDirectory = func() (string, error) { t.Error("validation read home"); return "", nil }
	for _, args := range [][]string{{"__supervise", "unknown", "0.1.2"}, {"__supervise", "t3code", "latest"}, {"__supervise", "t3code"}} {
		var out bytes.Buffer
		if status := manager.RunWithEffects(args, &out, &out, e); status != 2 {
			t.Fatalf("args=%q status=%d output=%s", args, status, &out)
		}
	}
	root := noProcesses(t, manager.Identity{UID: 0, GID: 0})
	root.HomeDirectory = func() (string, error) { t.Error("root supervisor read home"); return "", nil }
	var out bytes.Buffer
	if status := manager.RunWithEffects([]string{"__supervise", catalog.T3CodeTool, "0.1.2"}, &out, &out, root); status != 1 {
		t.Fatalf("root supervisor status=%d output=%s", status, &out)
	}
}

// lockFor takes the sandbox's manager lock in the test home, as a competing change would, and returns its release.
func lockFor(t *testing.T, home string) (func(), bool, error) {
	t.Helper()
	path := filepath.Join(home, ".local", "share", "sandboxed-agents", "manager.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, false, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, true, nil
}

// TestToolsStatusNeverReportsADeadServiceAsRunning proves a record that says running while a supervisor holds the lock but the service process is dead reports stopped with the restart note, at RunWithEffects with a real supervisor lock.
func TestToolsStatusNeverReportsADeadServiceAsRunning(t *testing.T) {
	home, run := t.TempDir(), &toolRun{}
	e, services := serviceEffects(t, home, toolRegistry{"latest": "0.1.2", "0.1.2": "0.1.2"}, run)
	runTools(t, e, 0, "enable", "t3code")
	stopFakeSupervisors(t, services)
	path := filepath.Join(toolDirectory(home), "service", "supervisor.lock")
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	state := readServiceStateFile(t, home)
	state["state"], state["service_pid"], state["last_failure"] = "running", 999999, "service exited after 0s: exit status 2"
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(toolDirectory(home), "service", "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got := readToolStatus(t, e)
	if got.ServiceState != "stopped" || !strings.Contains(got.ServiceFailure, "supervisor is restarting the service; last: service exited after 0s") {
		t.Fatalf("dead service reported as %+v", got)
	}
}
