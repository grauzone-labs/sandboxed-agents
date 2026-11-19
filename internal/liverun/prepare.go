package liverun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// prepare checks the checkout, engine, headroom and engine exclusion before publishing a plan.
func (s *Service) prepare(ctx context.Context, o Options) (Snapshot, error) {
	o.PreparationStart = time.Now().UTC()
	if err := os.MkdirAll(o.RunDir, 0700); err != nil {
		return Snapshot{}, err
	}
	if _, err := os.Stat(filepath.Join(o.RunDir, "started.json")); err == nil {
		return Snapshot{}, fmt.Errorf("run directory already used")
	}
	before, lock, err := s.prepareLocked(ctx, o)
	if err != nil {
		return before, err
	}
	defer lock.Close()
	if err := writeJSON(filepath.Join(o.RunDir, "options.json"), o); err != nil {
		return before, err
	}
	return before, writeJSON(filepath.Join(o.RunDir, "before.json"), before)
}

// prepareLocked verifies every pre-build check while keeping the selected engine excluded.
func (s *Service) prepareLocked(ctx context.Context, o Options) (Snapshot, *os.File, error) {
	if err := s.checkCoordination(o); err != nil {
		return Snapshot{}, nil, err
	}
	before := Snapshot{TakenAt: time.Now().UTC(), HostLoad: o.HostLoad}
	policy, policyErr := s.sessionPolicy()
	if policyErr == nil {
		policyErr = policy.launchable()
	}
	if policyErr != nil {
		return before, nil, failCheck(o, "session-detachment", policyErr)
	}
	before.Session = policy
	fail := func(name string, err error) (Snapshot, *os.File, error) { return before, nil, failCheck(o, name, err) }
	data, err := s.query(ctx, o, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(data)) != o.Revision {
		return fail("revision", fmt.Errorf("checkout revision differs: %v", err))
	}
	data, err = s.query(ctx, o, "git", "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
	if err != nil || strings.TrimSpace(string(data)) != "" {
		return fail("checkout-clean", fmt.Errorf("checkout has modified, untracked or ignored inputs: %v", err))
	}
	module, err := os.ReadFile(filepath.Join(o.Checkout, "go.mod"))
	if err != nil {
		return fail("go-pin", err)
	}
	pin := regexp.MustCompile("(?m)^go ([0-9]+\\.[0-9]+\\.[0-9]+)\\s*$").FindSubmatch(module)
	if len(pin) != 2 {
		return fail("go-pin", fmt.Errorf("go.mod must pin an exact patch version"))
	}
	data, err = s.query(ctx, o, "go", "env", "GOVERSION")
	if err != nil || strings.TrimSpace(string(data)) != "go"+string(pin[1]) {
		return fail("go-version", fmt.Errorf("effective Go toolchain differs from pinned version: %s; %v", data, err))
	}
	if err = os.MkdirAll(o.RecordDir, 0700); err != nil {
		return fail("record-directory", err)
	}
	platform := s.platformName()
	if _, err = os.Stat(filepath.Join(o.RecordDir, "live-suite-"+platform+".json")); err == nil || !os.IsNotExist(err) {
		return fail("record-directory", fmt.Errorf("record directory already contains suite evidence or is unreadable; use a fresh invocation directory"))
	}
	target, engine, err := s.target(ctx, o)
	if err != nil {
		return fail("engine", err)
	}
	before.Target, before.EngineID = target, engine
	lock, err := acquireEngine(o.LockRoot, engine)
	if err != nil {
		return fail("engine-exclusive", err)
	}
	lockedFail := func(name string, err error) (Snapshot, *os.File, error) { lock.Close(); return fail(name, err) }
	if err = refuseActive(o, engine); err != nil {
		return lockedFail("engine-exclusive", err)
	}
	before.Disks, err = s.disks(ctx, o, target)
	if err != nil {
		return lockedFail("filesystem", err)
	}
	data, err = s.podman(ctx, o, target, "system", "df")
	if err != nil {
		return lockedFail("storage-measurement", err)
	}
	before.SystemDF = string(data)
	if err = s.inventory(ctx, o, &before); err != nil {
		return lockedFail("inventory", err)
	}
	before.Complete = true
	if err = writeJSON(filepath.Join(o.RunDir, "preparation.json"), before); err != nil {
		return lockedFail("inventory-retention", err)
	}
	recordPath := o.PeakRecord
	if recordPath == "" {
		recordPath = peakPath(o, engine)
	}
	var peak PeakRecord
	err = readJSON(recordPath, &peak)
	if err != nil || !admissiblePeak(peak, before) || observedPeakExceeds(o, before, peak.PeakAdditionalBytes) {
		if !o.AllowUnbounded {
			lock.Close()
			return before, nil, headroomFailure(o, before, "peak-bound", 0)
		}
	} else {
		for _, disk := range before.Disks {
			if disk.FreeBytes <= peak.PeakAdditionalBytes {
				lock.Close()
				return before, nil, headroomFailure(o, before, "headroom", peak.PeakAdditionalBytes)
			}
		}
	}
	return before, lock, nil
}

// target selects exactly one running rootless engine without starting or repairing a machine.
func (s *Service) target(ctx context.Context, o Options) (Target, string, error) {
	var target Target
	if s.Platform == "windows" {
		data, err := s.query(ctx, o, "podman", "machine", "list", "--format", "json")
		if err != nil {
			return target, "", engineFault("engine unreachable", engineReason(err), err)
		}
		var machines []struct {
			Name    string
			Default bool
			VMType  string
		}
		if err = json.Unmarshal(data, &machines); err != nil || len(machines) == 0 {
			return target, "", engineFault("malformed target", "machine list unavailable or malformed", err)
		}
		chosen := machines[0]
		defaults := 0
		for _, candidate := range machines {
			if candidate.Default {
				chosen = candidate
				defaults++
			}
		}
		if len(machines) > 1 && defaults != 1 {
			return target, "", engineFault("ambiguous target", "select exactly one default Podman machine", nil)
		}
		if chosen.VMType != "wsl" || !validName(chosen.Name) {
			return target, "", engineFault("unsafe target", "a valid WSL2 machine selection is required", nil)
		}
		data, err = s.query(ctx, o, "podman", "machine", "inspect", chosen.Name)
		if err != nil {
			return target, "", engineFault("engine unreachable", engineReason(err), err)
		}
		var inspected []struct {
			Name, State string
			Rootful     *bool
			ConfigDir   struct{ Path string }
			SSHConfig   machineSSH
		}
		if err = json.Unmarshal(data, &inspected); err != nil || len(inspected) != 1 || inspected[0].Name != chosen.Name {
			return target, "", engineFault("malformed target", "machine inspect is malformed", err)
		}
		m := inspected[0]
		if m.State != "running" {
			_, engineErr := s.podman(ctx, o, Target{Connection: chosen.Name}, "info", "--format", "json")
			if engineErr != nil {
				return target, "", engineFault("machine stopped", "machine stopped; "+engineReason(engineErr), engineErr)
			}
		}
		if m.State != "running" {
			return target, "", engineFault("machine stopped", "machine stopped; start it outside this command", nil)
		}
		if m.Rootful == nil || *m.Rootful {
			return target, "", engineFault("unsafe target", "a verified rootless machine is required", fmt.Errorf("inspect answer: %s", data))
		}
		if m.SSHConfig.Port < 1 || m.SSHConfig.Port > 65535 || !regexp.MustCompile("^[a-zA-Z0-9_][a-zA-Z0-9_-]*$").MatchString(m.SSHConfig.RemoteUsername) || !windowsAbsolute(m.SSHConfig.IdentityPath) {
			return target, "", engineFault("malformed target", "machine filesystem or SSH metadata is malformed", nil)
		}
		target.Connection, target.MachineDiskPath, target.SSH = chosen.Name, m.ConfigDir.Path, m.SSHConfig
		if s.MachineDisk == nil {
			return target, "", engineFault("malformed target", "machine virtual disk location unverified", nil)
		}
		target.MachineDiskPath, err = s.MachineDisk(chosen.Name)
		if err != nil {
			return target, "", engineFault("malformed target", "machine virtual disk location unverified", err)
		}
	}
	data, err := s.podman(ctx, o, target, "info", "--format", "json")
	if err != nil {
		return target, "", engineFault("engine unreachable", engineReason(err), err)
	}
	identity, storage, err := EngineIdentity(data)
	if err != nil {
		return target, "", engineFault("malformed target", "engine storage identity is malformed", err)
	}
	target.Storage = storage
	return target, identity, nil
}

// windowsAbsolute accepts an absolute non-option Windows path without control characters.
func windowsAbsolute(path string) bool {
	return !strings.ContainsAny(path, "\r\n\x00") && ((len(path) > 2 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')) || strings.HasPrefix(path, "\\\\"))
}

// validName rejects names that could change process argument interpretation.
func validName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "-") && !strings.ContainsAny(name, "\r\n\x00")
}

// podman runs one query with the selected connection and no ambient Windows target overrides.
func (s *Service) podman(ctx context.Context, o Options, target Target, args ...string) ([]byte, error) {
	if target.Connection != "" {
		args = append([]string{"--connection", target.Connection}, args...)
	}
	return s.query(ctx, o, "podman", args...)
}

// disks reads engine storage and, on Windows, its host virtual-disk filesystem.
func (s *Service) disks(ctx context.Context, o Options, target Target) ([]Disk, error) {
	if s.Platform != "windows" {
		free, err := s.FreeSpace(target.Storage)
		return []Disk{{Name: "engine", Path: target.Storage, FreeBytes: free}}, err
	}
	host, err := s.FreeSpace(filepath.Dir(target.MachineDiskPath))
	if err != nil {
		return nil, err
	}
	private, err := os.MkdirTemp(o.RunDir, "ssh-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(private)
	path := "'" + strings.ReplaceAll(target.Storage, "'", "'\"'\"'") + "'"
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-F", "none", "-i", target.SSH.IdentityPath, "-p", strconv.Itoa(target.SSH.Port),
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "RequestTTY=no", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "UpdateHostKeys=no", "-o", "UserKnownHostsFile=known_hosts", "-o", "GlobalKnownHostsFile=none",
		"-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes",
		"-o", "CheckHostIP=no", "-o", "LogLevel=ERROR", target.SSH.RemoteUsername + "@localhost", "LC_ALL=C df -P -B1 -- " + path}
	data, err := s.Executor.Execute(bounded, Invocation{Program: "ssh", Args: args, Dir: private, Env: hostEnvironment(s.Platform, nil)})
	if err != nil {
		return nil, fmt.Errorf("machine free space query: %w; %s", err, data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("machine free space answer is invalid")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 6 {
		return nil, fmt.Errorf("machine free space answer is invalid")
	}
	free, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("machine available bytes are invalid")
	}
	return []Disk{{Name: "host", Path: target.MachineDiskPath, FreeBytes: host}, {Name: "engine", Path: target.Storage, FreeBytes: free}}, nil
}

// headroomFailure returns only filesystem roles and byte counts needed for the pending-host report.
func headroomFailure(o Options, before Snapshot, check string, bound uint64) error {
	parts := []string{}
	for _, disk := range before.Disks {
		parts = append(parts, fmt.Sprintf("%s %d bytes free", disk.Name, disk.FreeBytes))
	}
	expected := "no reliable bound"
	if bound > 0 {
		expected = fmt.Sprintf("%d bytes expected", bound)
	}
	_ = failCheck(o, check, fmt.Errorf("%s; %s", strings.Join(parts, "; "), expected))
	return fmt.Errorf("check %s failed; %s; %s", check, strings.Join(parts, "; "), expected)
}
