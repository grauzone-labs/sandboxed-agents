package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
)

// liveEngineFailures retains only typed facts, independently of the release record.
// Hosts must opt in explicitly; a timing-only fixture never starts diagnostic probes.
type liveEngineFailures struct {
	sync.Mutex
	directory string
	evidence  liveEngineFailureEvidence
}

// liveEngineFailureEvidence identifies supplemental observations from one suite.
type liveEngineFailureEvidence struct {
	SchemaVersion int                       `json:"schema_version"`
	Kind          string                    `json:"kind"`
	Commit        string                    `json:"commit"`
	Platform      string                    `json:"platform"`
	Samples       []liveEngineFailureSample `json:"samples"`
}

// liveEngineFailureSample contains fixed vocabulary and measured numeric facts.
// Nil means unverified; zero free memory or swap is a valid measured value.
type liveEngineFailureSample struct {
	Case            string  `json:"case"`
	Source          string  `json:"source"`
	Operation       string  `json:"operation"`
	Result          string  `json:"result"`
	EngineQuery     string  `json:"engine_query"`
	MachineQuery    string  `json:"machine_query"`
	MachineState    string  `json:"machine_state"`
	CPUs            *uint64 `json:"cpus,omitempty"`
	MemoryBytes     *uint64 `json:"memory_bytes,omitempty"`
	FreeMemoryBytes *uint64 `json:"free_memory_bytes,omitempty"`
	SwapBytes       *uint64 `json:"swap_bytes,omitempty"`
	FreeSwapBytes   *uint64 `json:"free_swap_bytes,omitempty"`
}

// liveDiagnosticOutput bounds private stdout without blocking or retaining overflow.
type liveDiagnosticOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

// Write consumes all bytes but retains at most 64 KiB for private JSON decoding.
func (output *liveDiagnosticOutput) Write(data []byte) (int, error) {
	n := min(len(data), 64*1024-output.buffer.Len())
	output.buffer.Write(data[:n])
	output.truncated = output.truncated || n != len(data)
	return len(data), nil
}

// observeFailure samples immediately before returning the original operation result.
// Diagnostic queries bypass this hook, so a failed query cannot recurse. Neither
// query nor persistence failures fail a case or replace the original error.
func (host liveHost) observeFailure(source, operation string, err error) {
	if err == nil || host.failures == nil {
		return
	}
	failures := host.failures
	// Reject accidental free-form attribution rather than ever publishing it.
	_, knownOperation := liveOperationBounds[operation]
	if (!slices.Contains(liveCheckNames, host.caseName) && host.caseName != "suite") || !knownOperation || !slices.Contains([]string{"cli", "podman"}, source) {
		return
	}
	sample := liveEngineFailureSample{Case: host.caseName, Source: source, Operation: operation, Result: "fail", EngineQuery: "unverified", MachineQuery: "not-applicable", MachineState: "unknown"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	selected := len(host.connection) == 2 && host.connection[0] == "--connection" && host.connection[1] != ""
	if failures.evidence.Platform == "windows-11" || selected {
		sample.MachineQuery = "unverified"
	}
	// Before Windows selection succeeds there is no verified engine target.
	// Do not query the ambient default or select another machine on a failure.
	if selected {
		local := host
		local.connection = nil
		data, ok := local.engineDiagnosticQuery(ctx, "machine", "inspect", host.connection[1])
		var machines []struct{ State string }
		if ok && json.Unmarshal(data, &machines) == nil && len(machines) == 1 && slices.Contains([]string{"running", "stopped", "starting", "stopping"}, machines[0].State) {
			sample.MachineQuery, sample.MachineState = "verified", machines[0].State
		}
	}
	if failures.evidence.Platform != "windows-11" || selected {
		data, ok := host.engineDiagnosticQuery(ctx, "info", "--format", "json")
		var info struct {
			Host struct {
				CPUs      *uint64 `json:"cpus"`
				MemTotal  *uint64 `json:"memTotal"`
				MemFree   *uint64 `json:"memFree"`
				SwapTotal *uint64 `json:"swapTotal"`
				SwapFree  *uint64 `json:"swapFree"`
			} `json:"host"`
		}
		if ok && json.Unmarshal(data, &info) == nil {
			facts := info.Host
			if facts.CPUs != nil && *facts.CPUs > 0 {
				sample.CPUs = facts.CPUs
			}
			if facts.MemTotal != nil && *facts.MemTotal > 0 {
				sample.MemoryBytes = facts.MemTotal
			}
			sample.FreeMemoryBytes, sample.SwapBytes, sample.FreeSwapBytes = facts.MemFree, facts.SwapTotal, facts.SwapFree
			if sample.CPUs != nil && sample.MemoryBytes != nil && sample.FreeMemoryBytes != nil && sample.SwapBytes != nil && sample.FreeSwapBytes != nil {
				sample.EngineQuery = "verified"
			}
		}
	}
	// Probe without the persistence lock: another failure must not defer this
	// snapshot until its own queries finish. Serialize only the atomic append/write.
	failures.Lock()
	defer failures.Unlock()
	failures.evidence.Samples = append(failures.evidence.Samples, sample)
	if failures.persist() != nil && host.report != nil {
		host.report("live engine failure evidence could not be persisted")
	}
}

// engineDiagnosticQuery runs one private read within three seconds plus a 250 ms
// drain, sharing the snapshot's eight-second budget. Every invocation contributes
// to #174 timing evidence; its output and error text never reach logs or artifacts.
func (host liveHost) engineDiagnosticQuery(parent context.Context, args ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	command := host.podmanCommand(ctx, args...)
	command.WaitDelay = 250 * time.Millisecond
	var output liveDiagnosticOutput
	started := time.Now()
	err := capture.Run(command, &output, io.Discard)
	host.recordOperation("podman", "inspection", started, err)
	return output.buffer.Bytes(), err == nil && ctx.Err() == nil && !output.truncated
}

// persist atomically replaces the artifact after each sample, before case cleanup,
// so later interruption cannot erase earlier observations. The caller holds the
// lock, except during initialization before the collector is shared.
func (failures *liveEngineFailures) persist() error {
	evidence := failures.evidence
	if !commitPattern.MatchString(evidence.Commit) || !slices.Contains([]string{"linux", "windows-11"}, evidence.Platform) {
		return errors.New("invalid engine failure evidence identity")
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(failures.directory, ".engine-failures-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), filepath.Join(failures.directory, "engine-failures-"+evidence.Platform+".json"))
}

// expectedLiveAbsence recognizes only the suite's normal negative existence probes.
func expectedLiveAbsence(args []string, err error) bool {
	exit, ok := err.(*exec.ExitError)
	return len(args) >= 2 && slices.Contains([]string{"container", "volume"}, args[0]) && args[1] == "exists" && ok && exit.ExitCode() == 1
}
