package main_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// liveToolObservation holds only the selection and independently observed service identity.
type liveToolObservation struct {
	Version, Channel, Pin string
	SupervisorPID         int   `json:"supervisor_pid"`
	ServicePID            int   `json:"service_pid"`
	PIDs                  []int `json:"pids"`
	UID, GID              int
	HTTPStatus            int `json:"http_status"`
}

// liveToolEvidence is supplemental sanitized evidence, separate from the release gate's fixed schema.
type liveToolEvidence struct {
	Commit       string               `json:"commit"`
	Platform     string               `json:"platform"`
	Result       string               `json:"result"`
	Observations []liveToolCheckpoint `json:"observations"`
}

// liveToolCheckpoint records one fixed checkpoint without process IDs, paths, logs, or account data.
type liveToolCheckpoint struct {
	Step    string `json:"step"`
	Version string `json:"version"`
	Channel string `json:"channel,omitempty"`
	Pin     string `json:"pin,omitempty"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}

// t3code exercises real installs, selection updates, detached service lifetime, sandbox replacement
// and rollback, deterministic failed staging, deletion, and re-enable without signing in anywhere.
func (suite *liveSuite) t3code(t *testing.T) {
	host := suite.host
	const name = "live-t3code"
	evidence := liveToolEvidence{Commit: suite.commit, Platform: suite.platform, Result: "fail", Observations: []liveToolCheckpoint{}}
	suite.writeToolEvidence(t, evidence)
	t.Cleanup(func() {
		if !t.Failed() {
			evidence.Result = "pass"
		}
		suite.writeToolEvidence(t, evidence)
	})
	host.cleanupSandbox(t, name)
	host.mustCLI(t, "", "up", name)
	baseline := host.inspectContainer(t, name)
	host.assertToolContainment(t, name, baseline)
	observe := func(step, channel, pin string) liveToolObservation {
		status := host.runningTool(t, name, channel, pin)
		evidence.Observations = append(evidence.Observations, liveToolCheckpoint{Step: step, Version: status.Version, Channel: status.Channel, Pin: status.Pin, UID: status.UID, GID: status.GID})
		current := host.inspectContainer(t, name)
		host.assertToolContainment(t, name, current)
		if !sameToolContainment(current, baseline) {
			t.Fatal("Tool or lifecycle operation changed the sandbox's networking, hardening, resources, or mounts")
		}
		return status
	}
	// The enable has a real terminal; observe only after its process exited and the terminal closed.
	func() {
		terminal := startLiveTerminal(t, host.environment, host.binary, "tools", "enable", name, "t3code")
		defer terminal.Close()
		if exit, finished := terminal.Wait(15 * time.Minute); !finished || exit != 0 {
			t.Fatalf("tools enable terminal: finished=%v exit=%d\n%s", finished, exit, terminal.Output())
		}
	}()
	observe("default-enable-terminal-closed", "latest", "")
	host.mustCLI(t, "", "tools", "update", name, "t3code")
	latest := observe("latest-update", "latest", "").Version
	host.mustCLI(t, "", "tools", "enable", name, "t3code", "--channel", "nightly")
	observe("nightly-enable", "nightly", "")
	host.mustCLI(t, "", "tools", "update", name, "t3code")
	observe("nightly-update", "nightly", "")
	host.mustCLI(t, "", "tools", "enable", name, "t3code", "--version", latest)
	observe("pin-enable", "", latest)
	host.mustCLI(t, "", "tools", "update", name, "t3code")
	beforeFailure := observe("pin-update", "", latest)
	host.failedToolUpdate(t, name)
	afterFailure := observe("failed-update-retained", "", latest)
	if beforeFailure.SupervisorPID != afterFailure.SupervisorPID || beforeFailure.ServicePID != afterFailure.ServicePID {
		t.Fatal("failed Tool staging replaced the previous running service")
	}
	for _, operation := range []string{"start", "restart", "up"} {
		if operation != "restart" {
			host.mustCLI(t, "", "stop", name)
		}
		host.mustCLI(t, "", operation, name)
		observe(operation+"-restored", "", latest)
	}
	output := host.mustCLI(t, "", "update", name, "--with", "native")
	if !strings.Contains(output, "updated") {
		t.Fatalf("sandbox replacement was not reported: %s", output)
	}
	replacement := host.inspectContainer(t, name)
	if sameImageID(replacement.Image, baseline.Image) || replacement.Config.Labels[cli.ToolchainsLabel] != "native" {
		t.Fatal("update did not replace the base sandbox with the native toolchain image")
	}
	observe("sandbox-update-restored", "", latest)
	failing := failingLiveUpdateHost(t, host, replacement.Config.Labels[cli.SSHPortLabel])
	output, err := failing.cli("", "update", name, "--with", "none", "--force")
	if err == nil || !strings.Contains(output, "readiness wait failed") || !strings.Contains(output, "restored sandbox") {
		t.Fatalf("forced sandbox rollback: %v %s", err, output)
	}
	restored := host.inspectContainer(t, name)
	if !sameImageID(restored.Image, replacement.Image) || restored.Config.Labels[cli.ToolchainsLabel] != "native" {
		t.Fatal("rollback did not restore the native toolchain image")
	}
	if output, err := host.podman("container", "exists", cli.DerivedNames(host.group, name).Backup); err == nil {
		t.Fatalf("rollback left a backup container: %s", output)
	}
	previous := observe("sandbox-rollback-restored", "", latest)
	host.mustCLI(t, "", "check", name)
	for attempt := range 2 {
		output := host.mustCLI(t, "", "tools", "delete", name, "t3code")
		if attempt == 1 && strings.TrimSpace(output) != "t3code not enabled; nothing to delete" {
			t.Fatalf("second Tool delete did not report its idempotent result: %s", output)
		}
		if output := host.mustCLI(t, "", "tools", "status", name, "t3code"); strings.TrimSpace(output) != "t3code: not enabled" {
			t.Fatalf("deleted Tool still enabled: %s", output)
		}
		args := []string{"deleted"}
		for _, pid := range previous.PIDs {
			args = append(args, strconv.Itoa(pid))
		}
		if output := host.toolProbe(t, name, args...); strings.TrimSpace(output) != `{"deleted":true}` {
			t.Fatalf("delete was not verified: %s", output)
		}
	}
	host.mustCLI(t, "", "tools", "enable", name, "t3code")
	observe("reenable-default", "latest", "")
	host.mustCLI(t, "", "remove", name, "--volumes")
	host.assertNoSandboxObjects(t, name)
}

// sameToolContainment compares the observed configuration without treating mount or option ordering as a change.
func sameToolContainment(left, right liveContainer) bool {
	for _, container := range []*liveContainer{&left, &right} {
		container.HostConfig.Binds = slices.Clone(container.HostConfig.Binds)
		for index, bind := range container.HostConfig.Binds {
			fields := strings.Split(bind, ":")
			if len(fields) == 3 {
				options := strings.Split(fields[2], ",")
				slices.Sort(options)
				fields[2] = strings.Join(options, ",")
				container.HostConfig.Binds[index] = strings.Join(fields, ":")
			}
		}
		slices.Sort(container.HostConfig.Binds)
		container.Mounts = slices.Clone(container.Mounts)
		slices.SortFunc(container.Mounts, func(a, b struct{ Type, Name, Source, Destination string }) int {
			return strings.Compare(a.Destination, b.Destination)
		})
	}
	return reflect.DeepEqual(left.HostConfig, right.HostConfig) && reflect.DeepEqual(left.Mounts, right.Mounts)
}

// writeToolEvidence replaces the sanitized supplemental file without adding fields to release records.
func (suite *liveSuite) writeToolEvidence(t *testing.T, evidence liveToolEvidence) {
	t.Helper()
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(suite.recordDirectory, "t3code-"+suite.platform+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatalf("write Tool evidence: %v", err)
	}
}

// toolProbe runs the test fixture as agent inside the disposable sandbox, with no host mount or credentials.
func (host liveHost) toolProbe(t *testing.T, name string, args ...string) string {
	t.Helper()
	script, err := os.ReadFile("testdata/live_t3code_probe.js")
	if err != nil {
		t.Fatal(err)
	}
	command := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", cli.DerivedNames(host.group, name).Container, "node", "-e", strings.ReplaceAll(string(script), "\n", " ")}
	output, err := host.podman(append(command, args...)...)
	if err != nil {
		t.Fatalf("Tool probe %v: %v\n%s", args, err, output)
	}
	return output
}

// runningTool compares public CLI status with a live HTTP listener and the supervisor's process tree identity.
func (host liveHost) runningTool(t *testing.T, name, channel, pin string) liveToolObservation {
	t.Helper()
	output := host.mustCLI(t, "", "tools", "status", name, "t3code")
	pattern := regexp.MustCompile(`(?m)^t3code: enabled; version (\S+); channel (\S+); pin (\S+); service running(?:;|$)`)
	match := pattern.FindStringSubmatch(output)
	wantChannel, wantPin := channel, pin
	if wantChannel == "" {
		wantChannel = "none"
	}
	if wantPin == "" {
		wantPin = "none"
	}
	if match == nil || catalog.ValidateVersion(match[1]) != nil || match[2] != wantChannel || match[3] != wantPin {
		t.Fatalf("Tool status did not retain channel %s / pin %s with a running service: %s", wantChannel, wantPin, output)
	}
	if pin != "" && match[1] != pin {
		t.Fatalf("pinned Tool runs %s; want %s", match[1], pin)
	}
	var status liveToolObservation
	output = host.toolProbe(t, name, "running")
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		t.Fatalf("read Tool probe: %v %s", err, output)
	}
	if status.Version != match[1] || status.Channel != channel || status.Pin != pin || status.UID != 1000 || status.GID != 1000 || status.HTTPStatus < 100 || status.HTTPStatus > 599 || status.SupervisorPID <= 1 || status.ServicePID <= 1 || !slices.Contains(status.PIDs, status.SupervisorPID) || !slices.Contains(status.PIDs, status.ServicePID) {
		t.Fatalf("Tool service observation disagrees with CLI status or containment: %+v", status)
	}
	return status
}

// failedToolUpdate forces a pinned repair to fail at staging, restores the fixture damage, and requires the named failure.
func (host liveHost) failedToolUpdate(t *testing.T, name string) {
	t.Helper()
	defer host.toolProbe(t, name, "repair-install")
	host.toolProbe(t, name, "break-install")
	output, err := host.cli("", "tools", "update", name, "t3code")
	if err == nil || !strings.Contains(output, ".next-") || !strings.Contains(strings.ToLower(output), "permission denied") {
		t.Fatalf("expected deterministic Tool staging failure: %v %s", err, output)
	}
}

// assertToolContainment proves the live sandbox retains SSH-only publishing and the original rootless hardening.
func (host liveHost) assertToolContainment(t *testing.T, name string, container liveContainer) {
	t.Helper()
	port := container.Config.Labels[cli.SSHPortLabel]
	bindings := container.HostConfig.PortBindings["22/tcp"]
	if !container.State.Running || container.Config.User != "0:0" || len(container.HostConfig.PortBindings) != 1 || len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != port || port == "" {
		t.Fatal("sandbox no longer publishes only SSH on its recorded loopback port")
	}
	for _, argument := range []string{"--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw"} {
		if !slices.Contains(container.Config.CreateCommand, argument) {
			t.Fatalf("live container creation omitted %s", argument)
		}
	}
	network, _, _ := strings.Cut(container.HostConfig.NetworkMode, ":")
	if network != "pasta" || container.HostConfig.Privileged || len(container.HostConfig.CapAdd) != 0 || len(container.HostConfig.Devices) != 0 || len(container.Mounts) != 3 {
		t.Fatal("sandbox has unexpected networking, privilege, devices, capabilities, or mounts")
	}
	names := cli.DerivedNames(host.group, name)
	want := map[string]string{"/workspace": names.Workspace, "/home/agent": names.Home, "/etc/ssh": names.SSH}
	// Podman includes named volumes in HostConfig.Binds as name:destination[:options].
	// Mounts independently proves their type, and no host path is accepted as a name here.
	if len(container.HostConfig.Binds) != 0 {
		remaining := maps.Clone(want)
		for _, bind := range container.HostConfig.Binds {
			fields := strings.Split(bind, ":")
			if len(fields) < 2 || len(fields) > 3 || remaining[fields[1]] == "" || remaining[fields[1]] != fields[0] {
				t.Fatal("sandbox has an unexpected HostConfig.Binds source or destination")
			}
			if len(fields) == 3 {
				for _, option := range strings.Split(fields[2], ",") {
					switch option {
					case "rw", "ro", "private", "rprivate", "bind", "rbind", "nosuid", "nodev", "exec", "noexec":
					default:
						t.Fatalf("sandbox has an unexpected HostConfig.Binds option %q", option)
					}
				}
			}
			delete(remaining, fields[1])
		}
		if len(remaining) != 0 {
			t.Fatal("sandbox HostConfig.Binds omitted an expected named volume")
		}
	}
	for _, mount := range container.Mounts {
		if mount.Type != "volume" || want[mount.Destination] == "" || want[mount.Destination] != mount.Name {
			t.Fatal("sandbox has an unexpected mount")
		}
		delete(want, mount.Destination)
	}
	if len(want) != 0 {
		t.Fatalf("sandbox omitted %d named volumes", len(want))
	}
	host.assertNoSwap(t, name, container)
}
