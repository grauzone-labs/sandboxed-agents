package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// liveHost runs the executable under test and reads Podman on the live host,
// in the suite's controller group, for every case of the live suite.
type liveHost struct {
	binary, group string
	environment   []string
	connection    []string
}

type liveImage struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string
	}
	RootFS struct {
		Layers []string
	}
}

// assertSSHServer checks PID 1 and its identity, then completes an unauthenticated
// exchange with the recorded loopback port and compares its key with server state.
func (host liveHost) assertSSHServer(t *testing.T, name, hostKey string) {
	t.Helper()
	container := host.inspectContainer(t, name)
	if !container.State.Running || container.Config.User != "0:0" {
		t.Fatalf("sandbox %s is not running as container root: %+v", name, container)
	}
	output, err := host.podman("exec", "--user=0:0", cli.DerivedNames(host.group, name).Container, "sh", "-c", "cat /proc/1/comm; cat /proc/1/status")
	if err != nil || !strings.HasPrefix(output, "sshd\n") || !regexp.MustCompile(`(?m)^Uid:\s+0\s+0\s+0\s+0$`).MatchString(output) {
		t.Fatalf("sandbox PID 1 is not root sshd: %v\n%s", err, output)
	}
	port := container.Config.Labels[cli.SSHPortLabel]
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		t.Fatalf("sandbox %s has invalid recorded SSH port %q", name, port)
	}
	bindings := container.HostConfig.PortBindings["22/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != port {
		t.Fatalf("sandbox SSH is not published only on its recorded loopback port: %+v", bindings)
	}
	directory := t.TempDir()
	keyFields := strings.Fields(hostKey)
	if len(keyFields) < 2 || keyFields[0] != "ssh-ed25519" {
		t.Fatalf("invalid server-state host key: %q", hostKey)
	}
	// Follow ADR-0008: a recorded key alone precedes signature verification.
	// NEWKEYS proves the exchange completed; authentication is disabled.
	deadline := time.Now().Add(15 * time.Second)
	var diagnostics []byte
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, "ssh", "-v", "-F", "none", "-p", port,
			"-o", "BatchMode=yes", "-o", "ConnectTimeout=2", "-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile=known_hosts", "-o", "GlobalKnownHostsFile=none",
			"-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "IdentityAgent=none",
			"-o", "PubkeyAuthentication=no", "-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no", "-o", "HostbasedAuthentication=no",
			"-o", "GSSAPIAuthentication=no", "agent@127.0.0.1")
		command.Dir = directory
		command.Env = host.environment
		command.WaitDelay = time.Second
		diagnostics, _ = command.CombinedOutput()
		cancel()
		known, readErr := os.ReadFile(filepath.Join(directory, "known_hosts"))
		fields := strings.Fields(string(known))
		if readErr == nil && len(fields) == 3 && fields[0] == "[127.0.0.1]:"+port &&
			fields[1] == keyFields[0] && fields[2] == keyFields[1] &&
			strings.Contains(string(diagnostics), "debug1: SSH2_MSG_NEWKEYS received") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no completed Ed25519 key exchange for %s on recorded port %s:\n%s", name, port, diagnostics)
}

// sshHostKey exercises the same administrative query used by SSH setup immediately after startup.
func (host liveHost) sshHostKey(t *testing.T, name string) string {
	t.Helper()
	args, err := cli.ManagerQueryArgs(cli.DerivedNames(host.group, name).Container, "/usr/local/bin/sandboxed-agents-manager", "ssh-host-key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := host.podmanCommand(ctx, args...)
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	key := strings.TrimSpace(string(output))
	if err != nil || !strings.HasPrefix(key, "ssh-ed25519 ") {
		t.Fatalf("read live SSH host key for %s: %v\n%s", name, err, output)
	}
	return key
}

// buildLiveExecutable compiles the host executable with the embedded manager
// into directory, as the documented development build does.
func buildLiveExecutable(t *testing.T, directory string) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binary := filepath.Join(directory, "sandboxed-agents"+suffix)
	for _, args := range [][]string{
		{"run", "./image/cmd/overlay", directory},
		{"build", "-overlay", filepath.Join(directory, "overlay.json"), "-o", binary, "./cmd/sandboxed-agents"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("prepare embedded live executable (go %v): %v\n%s", args, err, output)
		}
	}
	return binary
}

func (host liveHost) cli(input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, host.binary, args...)
	command.Env = host.environment
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (host liveHost) mustCLI(t *testing.T, input string, args ...string) string {
	t.Helper()
	t.Logf("sandboxed-agents %s", strings.Join(args, " "))
	output, err := host.cli(input, args...)
	if err != nil {
		t.Fatalf("sandboxed-agents %v: %v\n%s", args, err, output)
	}
	t.Log(output)
	return output
}

func (host liveHost) podman(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := host.podmanCommand(ctx, args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (host liveHost) podmanCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "podman", append(slices.Clone(host.connection), args...)...)
	command.Env = host.environment
	if runtime.GOOS == "windows" {
		var environment []string
		for _, entry := range command.Env {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, "CONTAINER_HOST") && !strings.EqualFold(key, "CONTAINER_CONNECTION") && !strings.EqualFold(key, "CONTAINER_SSHKEY") {
				environment = append(environment, entry)
			}
		}
		command.Env = environment
	}
	return command
}

func (host *liveHost) selectConnection(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	output, err := host.podman("machine", "list", "--format", "json")
	var machines []struct {
		Name    string
		Default bool
	}
	if err != nil || json.Unmarshal([]byte(output), &machines) != nil || len(machines) == 0 {
		t.Fatalf("select live Podman machine: %v\n%s", err, output)
	}
	chosen, defaults := machines[0].Name, 0
	for _, machine := range machines {
		if machine.Default {
			chosen = machine.Name
			defaults++
		}
	}
	if len(machines) > 1 && defaults != 1 {
		t.Fatal("live image inspection requires the command-selected default or sole Podman machine")
	}
	if chosen == "" || strings.HasPrefix(chosen, "-") || strings.ContainsAny(chosen, "\r\n\x00") {
		t.Fatalf("invalid Podman machine name %q", chosen)
	}
	// check already confirmed its running/rootless/WSL2 state. Bind every direct
	// image/container read to the same default-or-sole rule used by the CLI.
	host.connection = []string{"--connection", chosen}
}

type liveContainer struct {
	Image  string
	State  struct{ Running bool }
	Config struct {
		Image, User string
		Labels      map[string]string
	}
	HostConfig struct {
		PortBindings map[string][]struct{ HostIP, HostPort string }
	}
}

func (host liveHost) inspectContainer(t *testing.T, name string) liveContainer {
	t.Helper()
	output, err := host.podman("container", "inspect", cli.DerivedNames(host.group, name).Container)
	var records []liveContainer
	if err != nil || json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].Image == "" || records[0].Config.Image == "" {
		t.Fatalf("inspect live sandbox %s: %v\n%s", name, err, output)
	}
	return records[0]
}

func (host liveHost) inspectImage(t *testing.T, tag string) liveImage {
	t.Helper()
	output, err := host.podman("image", "inspect", tag)
	var records []liveImage
	if err != nil || json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].ID == "" {
		t.Fatalf("inspect live image %s: %v\n%s", tag, err, output)
	}
	return records[0]
}

func sameImageID(left, right string) bool {
	return strings.TrimPrefix(left, "sha256:") == strings.TrimPrefix(right, "sha256:")
}

func (host liveHost) cleanupSandbox(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		output, err := host.cli("", "remove", name, "--volumes", "--force")
		if err != nil && !strings.Contains(output, "does not exist in this controller group") {
			t.Errorf("cleanup sandbox %s: %v\n%s", name, err, output)
		}
		host.assertNoSandboxObjects(t, name)
	})
}

// assertNoSandboxObjects checks with Podman that no container, backup
// container, or volume of the sandbox remains.
func (host liveHost) assertNoSandboxObjects(t *testing.T, name string) {
	t.Helper()
	names := cli.DerivedNames(host.group, name)
	for _, resource := range []struct{ kind, name string }{
		{"container", names.Container}, {"container", names.Backup},
		{"volume", names.Workspace}, {"volume", names.Home}, {"volume", names.SSH},
	} {
		output, err := host.podman(resource.kind, "exists", resource.name)
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			continue
		}
		t.Errorf("left or could not verify %s %s: %v %s", resource.kind, resource.name, err, output)
	}
}

// assertNoGroupObjectsOnCleanup checks after every case that the controller
// group holds no container, backup container, or volume at all.
func (host liveHost) assertNoGroupObjectsOnCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { host.assertNoGroupObjects(t) })
}

// assertNoGroupObjects checks with Podman that the controller group holds no
// container, backup container, or volume.
func (host liveHost) assertNoGroupObjects(t *testing.T) {
	t.Helper()
	var leftovers []string
	for _, query := range [][]string{{"ps", "--all", "--format", "{{.Names}}"}, {"volume", "ls", "--format", "{{.Name}}"}} {
		output, err := host.podman(query...)
		if err != nil {
			t.Errorf("inventory of the controller group's objects: podman %v: %v\n%s", query, err, output)
			return
		}
		for _, name := range strings.Fields(output) {
			for _, prefix := range []string{"sandboxed-agents." + host.group + ".", "sandboxed-agents-backup." + host.group + "."} {
				if strings.HasPrefix(name, prefix) {
					leftovers = append(leftovers, name)
				}
			}
		}
	}
	if len(leftovers) != 0 {
		t.Errorf("objects of controller group %s remain: %v", host.group, leftovers)
	}
}

// assertListed checks the sandbox's state in list, or its absence when state is empty.
func (host liveHost) assertListed(t *testing.T, name, state string) {
	t.Helper()
	listing := host.mustCLI(t, "", "list")
	for _, row := range strings.Split(listing, "\n") {
		fields := strings.Fields(row)
		if len(fields) < 2 || fields[0] != name {
			continue
		}
		if state == "" {
			t.Fatalf("list still shows removed sandbox %s: %s", name, row)
		}
		if fields[1] != state {
			t.Fatalf("list shows %s as %s; want %s: %s", name, fields[1], state, row)
		}
		return
	}
	if state != "" {
		t.Fatalf("list omitted %s:\n%s", name, listing)
	}
}

func (host liveHost) removeGroupStateOnCleanup(t *testing.T) {
	t.Helper()
	root := os.Getenv("XDG_STATE_HOME")
	if runtime.GOOS == "windows" {
		root = os.Getenv("LOCALAPPDATA")
	} else if root == "" || !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("live host state root must be absolute: %q", root)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "sandboxed-agents", "group-"+host.group)); err != nil {
			t.Errorf("cleanup live controller group state: %v", err)
		}
	})
}
