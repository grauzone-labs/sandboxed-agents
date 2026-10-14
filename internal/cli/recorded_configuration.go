package cli

import "fmt"

type recordedIntegerLimit struct {
	option string
	value  *int64
}

func (container sandboxContainer) integerLimits() [3]recordedIntegerLimit {
	return [3]recordedIntegerLimit{
		{"memory", container.HostConfig.Memory},
		{"pids-limit", container.HostConfig.PidsLimit},
		{"shm-size", container.HostConfig.ShmSize},
	}
}

func (limit recordedIntegerLimit) read() (int64, error) {
	if limit.value == nil {
		return 0, fmt.Errorf("recorded --%s is missing", limit.option)
	}
	return *limit.value, nil
}

func (container sandboxContainer) workspaceBind() (string, bool) {
	for _, mount := range container.Mounts {
		if mount.Type == "bind" && mount.Destination == "/workspace" {
			return mount.Source, true
		}
	}
	return "", false
}

func recordedCPUs(container sandboxContainer) (float64, error) {
	limits := container.HostConfig
	if limits.CpuQuota != nil && limits.CpuPeriod != nil && *limits.CpuPeriod > 0 && *limits.CpuQuota > 0 {
		return float64(*limits.CpuQuota) / float64(*limits.CpuPeriod), nil
	}
	if limits.NanoCpus != nil && *limits.NanoCpus > 0 {
		return float64(*limits.NanoCpus) / 1e9, nil
	}
	return 0, fmt.Errorf("recorded --cpus is missing")
}
