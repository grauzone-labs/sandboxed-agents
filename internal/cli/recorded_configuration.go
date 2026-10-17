package cli

import (
	"fmt"
	"math"
)

type recordedIntegerLimit struct {
	option string
	value  *int64
}

// integerLimits returns the recorded memory, process, shared-memory, and combined memory/swap limits.
func (container sandboxContainer) integerLimits() [4]recordedIntegerLimit {
	return [4]recordedIntegerLimit{
		{"memory", container.HostConfig.Memory},
		{"pids-limit", container.HostConfig.PidsLimit},
		{"shm-size", container.HostConfig.ShmSize},
		{"memory-swap", container.HostConfig.MemorySwap},
	}
}

// read returns a recorded limit or names the missing inspect field.
func (limit recordedIntegerLimit) read() (int64, error) {
	if limit.value == nil {
		return 0, fmt.Errorf("recorded --%s is missing", limit.option)
	}
	return *limit.value, nil
}

// workspaceBind returns the host source of a bind at /workspace, if one exists.
func (container sandboxContainer) workspaceBind() (string, bool) {
	for _, mount := range container.Mounts {
		if mount.Type == "bind" && mount.Destination == "/workspace" {
			return mount.Source, true
		}
	}
	return "", false
}

type recordedCPULimit struct {
	units, period int64
}

// cpuLimit reads the CPU quota and period, falling back to the NanoCpus representation.
func (container sandboxContainer) cpuLimit() (recordedCPULimit, error) {
	limits := container.HostConfig
	if limits.CpuQuota != nil && limits.CpuPeriod != nil && *limits.CpuPeriod > 0 && *limits.CpuQuota > 0 {
		return recordedCPULimit{*limits.CpuQuota, *limits.CpuPeriod}, nil
	}
	if limits.NanoCpus != nil && *limits.NanoCpus > 0 {
		return recordedCPULimit{*limits.NanoCpus, 1e9}, nil
	}
	return recordedCPULimit{}, fmt.Errorf("recorded --cpus is missing")
}

// cores returns the effective CPU count represented by the recorded integer units.
func (limit recordedCPULimit) cores() float64 {
	return float64(limit.units) / float64(limit.period)
}

// displayCores recovers a matching CLI override or preserves an externally configured ratio.
func (limit recordedCPULimit) displayCores() float64 {
	cores := limit.cores()
	requested := math.Round(cores*1000) / 1000
	// Recover a supported three-decimal override only when it yields this
	// recorded quota. Keep the actual ratio for externally configured limits.
	if limit.matches(requested) {
		return requested
	}
	return cores
}

// matches compares requested cores in the integer units used by the recorded CPU limit.
func (limit recordedCPULimit) matches(cpus float64) bool {
	if limit.cores() == cpus {
		return true
	}
	// Podman truncates floating point cores to whole quota units. Compare in
	// those units instead of requiring its rounded ratio to equal the input.
	// https://github.com/containers/podman/blob/v4.4.0/pkg/util/utils.go
	units := cpus * float64(limit.period)
	return units < math.Exp2(63) && int64(units) == limit.units
}
