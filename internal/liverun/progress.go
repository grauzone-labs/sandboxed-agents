package liverun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// progressCaseLine admits only the suite's fixed case names.
var progressCaseLine = regexp.MustCompile("TestLiveSuite/(" + strings.Join(validationduration.OperationCases(), "|") + ")(?:\\s|$|/)")

// progressClasses and progressCases retain private copies of the authoritative timing vocabulary.
var progressClasses, progressCases = validationduration.OperationClasses(), validationduration.OperationCases()

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
	lastSave    time.Time
	logFailed   bool
}

// Write forwards the exact bytes privately and classifies only complete log lines.
func (w *progressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.destination.Write(data)
	if err != nil || n != len(data) {
		w.logFailed = true
	}
	// A private I/O failure cannot stop draining the suite's stdout/stderr pipes.
	before := w.state
	w.state.LastOutput = time.Now().UTC()
	w.pending = append(w.pending, data...)
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
	if before.Case != w.state.Case || before.Operation != w.state.Operation || time.Since(w.lastSave) >= time.Second {
		w.lastSave = time.Now()
		_ = writeJSON(w.filename, w.state)
	}
	return len(data), nil
}

// observe classifies only fixed suite cases and known command verbs, dropping all object identities.
func (w *progressWriter) observe(line string) {
	line = strings.TrimSpace(buildLogPrefix.ReplaceAllString(strings.TrimSpace(line), ""))
	if strings.HasPrefix(line, "live output: ") {
		return
	}
	if tail, ok := strings.CutPrefix(line, "live operation "); ok {
		class, phase, found := strings.Cut(tail, ": ")
		if found && fixedOperation(class) && (strings.HasPrefix(phase, "started") || strings.HasPrefix(phase, "finished after ")) {
			w.state.Operation = class
			return
		}
	}
	if match := progressCaseLine.FindStringSubmatch(line); len(match) > 1 {
		w.state.Case = match[1]
	}
	marker := "sandboxed-agents "
	if tail, ok := strings.CutPrefix(line, marker); ok {
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
	ended, finished, err := completion(directory, nil)
	if err != nil {
		return fmt.Errorf("completion evidence unverified")
	}
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
			ended, finished, err = completion(directory, &exitEvidence{Code: 137, Interrupted: true, EndedAt: time.Now().UTC()})
			if err != nil {
				return fmt.Errorf("cannot retain interrupted exit evidence")
			}
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
		state["log_verified"] = !ended.LogUnverified
		var peak PeakRecord
		state["peak_recorded"] = !ended.PeakUnverified && readJSON(filepath.Join(directory, "peak.json"), &peak) == nil && peak.EngineID == before.EngineID && peak.Revision == o.Revision
		var bound PeakRecord
		boundPath := o.PeakRecord
		if boundPath == "" {
			boundPath = peakPath(o, before.EngineID)
		}
		state["peak_bound_available"] = readJSON(boundPath, &bound) == nil && admissiblePeak(bound, before) && !observedPeakExceeds(o, before, bound.PeakAdditionalBytes)
	}
	return json.NewEncoder(out).Encode(state)
}

// fixedCase reports whether a status value is a documented suite case.
func fixedCase(name string) bool {
	return slices.Contains(progressCases, name)
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
	if readCompletion(filepath.Join(directory, "exit.json"), &ended) != nil {
		return fmt.Errorf("suite has not ended; collect after status confirms its exit")
	}
	if ended.LogUnverified {
		return fmt.Errorf("private log unverified; durations collection unavailable")
	}
	log, err := os.ReadFile(filepath.Join(directory, "suite.log"))
	if err != nil {
		return fmt.Errorf("private log unavailable")
	}
	asset, _ := parseBuildAsset(log)
	load, base, cache := classifyContext(before, asset)
	platform := s.platformName()
	record := filepath.Join(o.RecordDir, "live-suite-"+platform+".json")
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	collectorArgs := []string{"run", "./internal/validationduration/cmd/retain-durations", "-record", record, "-host-load", load, "-base-image", base, "-build-cache", cache}
	var started startedRun
	if readJSON(filepath.Join(directory, "started.json"), &started) == nil && !started.PreparationStart.IsZero() && !started.PreparationEnd.IsZero() {
		collectorArgs = append(collectorArgs, "-preparation-start", started.PreparationStart.Format(time.RFC3339Nano), "-preparation-end", started.PreparationEnd.Format(time.RFC3339Nano))
	}
	data, err := s.Executor.Execute(bounded, Invocation{Program: "go", Dir: o.Checkout, Env: hostEnvironment(s.Platform, nil), Input: log, Args: collectorArgs})
	expected := filepath.Join(o.RecordDir, "durations-"+platform+".json")
	collected, optional := collectorOutcome(data, err, expected)
	if !collected {
		return failCheck(o, "durations-collector", fmt.Errorf("collector did not confirm this invocation output"))
	}
	return json.NewEncoder(out).Encode(map[string]any{"durations_file": filepath.Base(expected), "collected": true, "optional_operation_evidence_unverified": optional, "host_load": load, "base_image": base, "build_cache": cache})
}

// fixedOperation admits only the documented sanitized operation categories.
func fixedOperation(operation string) bool {
	return slices.Contains(progressClasses, operation)
}
