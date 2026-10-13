package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Host supplies platform facts and read-only file/user queries to the command
// boundary, plus a transient loopback availability probe used after preflight.
// It does not replace Podman execution or authorize persistent host mutations.
type Host struct {
	Platform, Architecture string
	ReadFile               func(string) ([]byte, error)
	CurrentUser            func() (*user.User, error)
	WindowsBuild           func() (uint32, error)
	WindowsWorkstation     func() (bool, error)
	PortAvailable          func(uint16) error
}

func (h Host) defaults() Host {
	if h.Platform == "" {
		h.Platform = runtime.GOOS
	}
	if h.Architecture == "" {
		h.Architecture = runtime.GOARCH
	}
	if h.ReadFile == nil {
		h.ReadFile = os.ReadFile
	}
	if h.CurrentUser == nil {
		h.CurrentUser = user.Current
	}
	if h.WindowsBuild == nil {
		h.WindowsBuild = windowsBuild
	}
	if h.WindowsWorkstation == nil {
		h.WindowsWorkstation = windowsWorkstation
	}
	if h.PortAvailable == nil {
		h.PortAvailable = loopbackPortAvailable
	}
	return h
}

func preflight(h Host) (podmanTarget, error) {
	p := podmanTarget{host: h}
	if h.Platform == "windows" {
		return windowsPreflight(p)
	}
	if h.Platform != "linux" || h.Architecture != "amd64" {
		return p, fmt.Errorf("supported hosts are Linux amd64 and Windows 11 x64")
	}
	return p, linuxPreflight(h)
}

func linuxPreflight(h Host) error {
	var failures []error
	requireProgram := func(name string) bool {
		if _, err := exec.LookPath(name); err != nil {
			failures = append(failures, fmt.Errorf("%s is required on PATH", name))
			return false
		}
		return true
	}
	if requireProgram("podman") {
		minor := 4
		output, err := (podmanTarget{host: h}).query("--version")
		if err != nil || !atLeast(output, 4, minor, 0) {
			failures = append(failures, fmt.Errorf("Podman >= 4.%d.0 is required (client version: %q; query: %v)", minor, strings.TrimSpace(output), err))
		}
		info, err := (podmanTarget{host: h}).info()
		if err != nil {
			failures = append(failures, err)
		} else {
			if info.Host.Security.Rootless == nil || !*info.Host.Security.Rootless {
				failures = append(failures, fmt.Errorf("rootless Podman is required"))
			}
			if info.Host.CgroupVersion != "v2" {
				failures = append(failures, fmt.Errorf("cgroups v2 is required"))
			}
		}
	}
	for _, name := range []string{"newuidmap", "newgidmap", "pasta", "ssh", "ssh-keygen"} {
		requireProgram(name)
	}
	account, err := h.CurrentUser()
	if err != nil || account == nil {
		failures = append(failures, fmt.Errorf("read current user for subordinate ranges: %v", err))
	} else {
		if !subordinateRange(h, "/etc/subuid", account) {
			failures = append(failures, fmt.Errorf("a subordinate UID range is required in /etc/subuid"))
		}
		if !subordinateRange(h, "/etc/subgid", account) {
			failures = append(failures, fmt.Errorf("a subordinate GID range is required in /etc/subgid"))
		}
		controllers, err := delegatedControllers(h, account.Uid)
		failures = append(failures, controllerFailures(controllers, err)...)
	}
	return errors.Join(failures...)
}

func controllerFailures(controllers string, queryErr error) []error {
	var failures []error
	if queryErr != nil {
		failures = append(failures, queryErr)
	}
	for _, name := range []string{"cpu", "memory", "pids"} {
		if !slices.Contains(strings.Fields(controllers), name) {
			failures = append(failures, fmt.Errorf("delegated cgroup %s controller is required", name))
		}
	}
	return failures
}

func subordinateRange(h Host, path string, account *user.User) bool {
	data, err := h.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) != 3 || (parts[0] != account.Username && parts[0] != account.Uid) {
			continue
		}
		start, e1 := strconv.ParseUint(parts[1], 10, 32)
		count, e2 := strconv.ParseUint(parts[2], 10, 32)
		if e1 == nil && e2 == nil && count > 0 && start+count <= 1<<32 {
			return true
		}
	}
	return false
}

func delegatedControllers(h Host, uid string) (string, error) {
	data, err := h.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read delegated cgroup controllers: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		location, ok := strings.CutPrefix(line, "0::")
		if !ok || !strings.HasPrefix(location, "/") {
			continue
		}
		location = path.Clean(location)
		if location == "/" {
			return "", fmt.Errorf("read delegated cgroup controllers: host root is not a user delegation")
		}
		// Login sessions are siblings of the user manager under user-UID.slice.
		// That identity-specific slice also identifies the delegation to query.
		userSlice := "/user.slice/user-" + uid + ".slice"
		if location == userSlice || strings.HasPrefix(location, userSlice+"/") {
			location = userSlice + "/user@" + uid + ".service"
		}
		// systemd delegates the user manager, rather than an application's leaf
		// scope. Only use that ancestor when the process actually belongs to it.
		marker := "/user@" + uid + ".service"
		if index := strings.Index(location, marker); index >= 0 {
			end := index + len(marker)
			if end == len(location) || location[end] == '/' {
				location = location[:end]
			}
		}
		// A non-systemd host is checked at its actual unified cgroup. We never
		// walk to the host root where controllers may exist without delegation.
		controllers, err := h.ReadFile(path.Join("/sys/fs/cgroup", location, "cgroup.controllers"))
		if err != nil {
			return "", fmt.Errorf("read delegated cgroup controllers: %w", err)
		}
		return string(controllers), nil
	}
	return "", fmt.Errorf("read delegated cgroup controllers: no unified cgroup")
}

type podmanInfo struct {
	Host struct {
		Security      struct{ Rootless *bool }
		CgroupVersion string
	}
}

func (p podmanTarget) info() (podmanInfo, error) {
	var info podmanInfo
	// JSON exposes host.security.rootless and host.cgroupVersion without parsing
	// human output. Info does not prove delegation; controller files are checked
	// separately at the current user's delegated cgroup.
	output, err := p.query("info", "--format", "json")
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return info, fmt.Errorf("read Podman info: %w", err)
	}
	return info, nil
}

type podmanTarget struct {
	host       Host
	connection string
}

func (p podmanTarget) query(args ...string) (string, error) {
	if p.connection != "" {
		args = append([]string{"--connection", p.connection}, args...)
	}
	command := exec.CommandContext(context.Background(), "podman", args...)
	command.Env = os.Environ()
	if p.host.Platform == "windows" {
		command.Env = nil
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			switch strings.ToUpper(key) {
			case "CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY":
				continue
			}
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("podman %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

var versionPattern = regexp.MustCompile(`(?:^|\s)([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+\s]|$)`)

func atLeast(value string, major, minor, patch int) bool {
	parts := versionPattern.FindStringSubmatch(value)
	if len(parts) != 4 {
		return false
	}
	for i, want := range []int{major, minor, patch} {
		got, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return false
		}
		if got != want {
			return got > want
		}
	}
	return true
}
