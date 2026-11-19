package liverun

import "time"

// Snapshot is the private whole-engine inventory retained before any build.
type Snapshot struct {
	Complete   bool
	EngineID   string
	Target     Target
	Images     []Image
	Containers []Container
	Disks      []Disk
	SystemDF   string
	HostLoad   string
	TakenAt    time.Time
	Session    SessionPolicy
}

// Disk retains one filesystem's private identity and available bytes.
type Disk struct {
	Name      string
	Path      string
	FreeBytes uint64
}

// Image preserves exact IDs, labels, tags and creation times for later image cleanup.
type Image struct {
	ID       string
	Labels   map[string]string
	Created  time.Time
	RepoTags []string
}

// Container preserves each container's exact full image reference across the whole engine.
type Container struct {
	ID    string
	Image string
	State struct{ Status string }
}

// PeakRecord separates sampled filesystem facts from an optional maintainer-reasoned headroom bound.
type PeakRecord struct {
	EngineID              string
	PeakAdditionalBytes   uint64
	Basis                 string
	Revision              string
	SampleStartedAt       time.Time
	SampleEndedAt         time.Time
	SampleIntervalSeconds float64
	SampleContext         string
	SamplingComplete      bool
	Filesystems           []PeakFilesystem
}

// Target binds one selected engine and its read-only machine query endpoint.
type Target struct {
	Connection      string
	Storage         string
	MachineDiskPath string
	SSH             machineSSH
}

// machineSSH names Podman's inspected loopback SSH endpoint.
type machineSSH struct {
	IdentityPath   string
	Port           int
	RemoteUsername string
}

// SessionPolicy retains sanitized facts about the launching process's immediate Windows job.
// An immediate-job observation cannot attest the limits of outer nested jobs.
type SessionPolicy struct{ InJob, KillOnClose, Breakaway, SilentBreakaway bool }
