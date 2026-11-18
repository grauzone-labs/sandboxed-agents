package liverun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// progressState retains only allowlisted case names and operation categories from private output.
type progressState struct {
	Case       string
	Operation  string
	LastOutput time.Time
}

// progressWriter serializes private UTF-8 log writes and retains a sanitized status observation.
type progressWriter struct {
	mu          sync.Mutex
	destination io.Writer
	filename    string
	pending     []byte
	state       progressState
}

// Write forwards the exact bytes privately and classifies only complete log lines.
func (w *progressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.destination.Write(data)
	if n > 0 {
		w.state.LastOutput = time.Now().UTC()
		w.pending = append(w.pending, data[:n]...)
		for {
			end := bytes.IndexByte(w.pending, '\n')
			if end < 0 {
				break
			}
			w.observe(string(w.pending[:end]))
			w.pending = w.pending[end+1:]
		}
		if len(w.pending) > 1024*1024 {
			w.pending = nil
		}
		if saveErr := writeJSON(w.filename, w.state); saveErr != nil && err == nil {
			err = saveErr
		}
	}
	return n, err
}

// observe classifies only fixed suite cases and known command verbs, dropping all object identities.
func (w *progressWriter) observe(line string) {
	if _, tail, ok := strings.Cut(line, "live operation "); ok {
		class, phase, found := strings.Cut(tail, ": ")
		if found && fixedOperation(class) && (strings.HasPrefix(phase, "started") || strings.HasPrefix(phase, "finished after ")) {
			w.state.Operation = class
			return
		}
	}
	if match := regexp.MustCompile("TestLiveSuite/(preflight|lifecycle|update|agent|t3code|images)(?:\\s|$|/)").FindStringSubmatch(line); len(match) > 1 {
		w.state.Case = match[1]
	}
	marker := "sandboxed-agents "
	if _, tail, ok := strings.Cut(line, marker); ok {
		words := strings.Fields(tail)
		if len(words) == 0 {
			return
		}
		for len(words) > 0 && strings.HasPrefix(words[0], "-") {
			words = words[1:]
		}
		if len(words) == 0 {
			return
		}
		switch words[0] {
		case "up":
			w.state.Operation = "image build or sandbox start"
		case "stop", "start", "restart":
			w.state.Operation = "sandbox start or restart"
		case "ssh-config":
			w.state.Operation = "SSH server check"
		case "shell":
			w.state.Operation = "smoke"
		case "update":
			w.state.Operation = "update or rollback"
		case "build":
			w.state.Operation = "rebuild"
		case "remove":
			w.state.Operation = "cleanup"
		case "agents":
			if len(words) > 1 {
				switch words[1] {
				case "enable":
					w.state.Operation = "agent install"
				case "session", "disable":
					w.state.Operation = "agent session"
				}
			}
		case "tools":
			if len(words) > 1 {
				switch words[1] {
				case "enable":
					w.state.Operation = "Tool install"
				case "update":
					w.state.Operation = "Tool update"
				case "delete":
					w.state.Operation = "Tool delete"
				case "status":
					w.state.Operation = "Tool probe"
				}
			}
		}
	}
}

// status emits a path-free current observation and fresh filesystem headroom.
func (s *Service) status(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	directory, err := runDirectory(args, diagnostics)
	if err != nil {
		return err
	}
	var o Options
	var before Snapshot
	if readJSON(filepath.Join(directory, "options.json"), &o) != nil || readJSON(filepath.Join(directory, "before.json"), &before) != nil {
		return fmt.Errorf("prepared run is unavailable")
	}
	var progress progressState
	_ = readJSON(filepath.Join(directory, "progress.json"), &progress)
	if !fixedCase(progress.Case) {
		progress.Case = "unknown"
	}
	if !fixedOperation(progress.Operation) {
		progress.Operation = "unknown"
	}
	var ended exitEvidence
	finished := readJSON(filepath.Join(directory, "exit.json"), &ended) == nil
	var started startedRun
	running := false
	if !finished && readJSON(filepath.Join(directory, "started.json"), &started) == nil {
		var active activeRun
		activeErr := readJSON(filepath.Join(directory, "processes.json"), &active)
		if activeErr != nil {
			activeErr = readJSON(filepath.Join(directory, "launching.json"), &active)
		}
		observed := activeErr == nil && active.RunDir == directory
		if observed {
			running = active.Supervisor.live() || suiteAlive(active.Child)
		}
		if observed && !running {
			ended = exitEvidence{Code: 137, Interrupted: true, EndedAt: time.Now().UTC()}
			if err = writeExitText(directory, 137); err != nil {
				return fmt.Errorf("cannot retain interrupted exit status")
			}
			if err = writeJSON(filepath.Join(directory, "exit.json"), ended); err != nil {
				return fmt.Errorf("cannot retain interrupted exit evidence")
			}
			finished = true
		}
	}
	disks, err := s.disks(ctx, o, before.Target)
	if err != nil {
		return failCheck(o, "filesystem", err)
	}
	summary := []map[string]any{}
	for _, disk := range disks {
		summary = append(summary, map[string]any{"filesystem": disk.Name, "free_bytes": disk.FreeBytes})
	}
	age := int64(-1)
	if !progress.LastOutput.IsZero() {
		age = int64(time.Since(progress.LastOutput).Seconds())
		if age < 0 {
			age = 0
		}
	}
	state := map[string]any{"case": progress.Case, "operation": progress.Operation, "last_output_age_seconds": age, "ended": finished, "running": running, "free_space": summary}
	if finished {
		state["exit_code"] = ended.Code
		state["interrupted"] = ended.Interrupted
	}
	return json.NewEncoder(out).Encode(state)
}

// fixedCase reports whether a status value is a documented suite case.
func fixedCase(name string) bool {
	switch name {
	case "preflight", "lifecycle", "update", "agent", "t3code", "images":
		return true
	}
	return false
}

// classifyContext derives categorical comparison values from the before snapshot alone.
func classifyContext(before Snapshot, asset string) (string, string, string) {
	load := before.HostLoad
	if load != "idle" && load != "shared" {
		load = "unknown"
	}
	base, cache := "unknown", "unknown"
	if !before.Complete || asset == "" {
		return load, base, cache
	}
	base = "absent"
	anyManaged, anyUntagged, warm := false, false, false
	for _, image := range before.Images {
		labels := image.Labels
		if len(image.RepoTags) == 0 || len(image.RepoTags) == 1 && image.RepoTags[0] == "<none>:<none>" {
			anyUntagged = true
		}
		if _, ok := labels["io.sandboxed-agents.image"]; ok {
			anyManaged = true
			if labels["io.sandboxed-agents.asset-sha256"] == asset {
				warm = true
				if labels["io.sandboxed-agents.toolchains"] == "none" {
					base = "present"
				}
			}
		}
	}
	if warm {
		cache = "warm"
	} else if !anyManaged && !anyUntagged {
		cache = "cold"
	}
	return load, base, cache
}

// collect runs the existing durations collector from the private log with snapshot-derived context.
func (s *Service) collect(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	directory, err := runDirectory(args, diagnostics)
	if err != nil {
		return err
	}
	var o Options
	var before Snapshot
	var ended exitEvidence
	if readJSON(filepath.Join(directory, "options.json"), &o) != nil || readJSON(filepath.Join(directory, "before.json"), &before) != nil {
		return fmt.Errorf("prepared run is unavailable")
	}
	if readJSON(filepath.Join(directory, "exit.json"), &ended) != nil {
		return fmt.Errorf("suite has not ended; collect after status confirms its exit")
	}
	log, err := os.ReadFile(filepath.Join(directory, "suite.log"))
	if err != nil {
		return fmt.Errorf("private log unavailable")
	}
	asset := ""
	if match := regexp.MustCompile("(?m)asset-sha256 ([0-9a-f]{64})(?:\\s|$)").FindSubmatch(log); len(match) == 2 {
		asset = string(match[1])
	}
	load, base, cache := classifyContext(before, asset)
	platform := "linux"
	if s.Platform == "windows" {
		platform = "windows-11"
	}
	record := filepath.Join(o.RecordDir, "live-suite-"+platform+".json")
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	collectorArgs := []string{"run", "./internal/validationduration/cmd/retain-durations", "-record", record, "-host-load", load, "-base-image", base, "-build-cache", cache}
	var started startedRun
	if readJSON(filepath.Join(directory, "started.json"), &started) == nil && !started.PreparationStart.IsZero() && !started.PreparationEnd.IsZero() {
		collectorArgs = append(collectorArgs, "-preparation-start", started.PreparationStart.Format(time.RFC3339Nano), "-preparation-end", started.PreparationEnd.Format(time.RFC3339Nano))
	}
	data, err := s.Executor.Execute(bounded, Invocation{Program: "go", Dir: o.Checkout, Env: hostEnvironment(s.Platform, nil), Input: log, Args: collectorArgs})
	if err != nil {
		return failCheck(o, "durations-collector", fmt.Errorf("%w; %s", err, data))
	}
	expected := filepath.Join(o.RecordDir, "durations-"+platform+".json")
	if strings.TrimSpace(string(data)) != expected {
		return failCheck(o, "durations-collector", fmt.Errorf("collector did not confirm this invocation output"))
	}
	return json.NewEncoder(out).Encode(map[string]any{"durations_file": filepath.Base(expected), "collected": true, "host_load": load, "base_image": base, "build_cache": cache})
}

// fixedOperation admits only the documented sanitized operation categories.
func fixedOperation(operation string) bool {
	switch operation {
	case "preflight", "inspection", "image build", "image build or sandbox start", "sandbox start or restart", "SSH server check", "smoke", "update or rollback", "rebuild", "cleanup", "agent install", "agent session", "Tool install", "Tool update", "Tool delete", "Tool probe":
		return true
	}
	return false
}
