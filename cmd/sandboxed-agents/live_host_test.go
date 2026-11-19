package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/capture"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/preflighttiming"
)

// liveHost runs the executable under test and reads Podman on the live host,
// in the suite's controller group, for every case of the live suite.
type liveHost struct {
	binary, group   string
	environment     []string
	connection      []string
	ledger          *preflighttiming.Ledger
	timings         *liveTimings
	failures        *liveEngineFailures
	caseName        string
	removals        chan struct{}
	operationBounds map[string]time.Duration
	report          func(string)
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
		t.Fatalf("sandbox %s is not running as container root: %#v", name, container)
	}
	output, err := host.podmanOperation("SSH server check", "exec", "--user=0:0", cli.DerivedNames(host.group, name).Container, "sh", "-c", "cat /proc/1/comm; cat /proc/1/status")
	if err != nil || !strings.HasPrefix(output, "sshd\n") || !regexp.MustCompile(`(?m)^Uid:\s+0\s+0\s+0\s+0$`).MatchString(output) {
		t.Fatalf("sandbox PID 1 is not root sshd: %v\n%q", err, output)
	}
	port := container.Config.Labels[cli.SSHPortLabel]
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		t.Fatalf("sandbox %s has invalid recorded SSH port %q", name, port)
	}
	bindings := container.HostConfig.PortBindings["22/tcp"]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != port {
		t.Fatalf("sandbox SSH is not published only on its recorded loopback port: %#v", bindings)
	}
	directory := t.TempDir()
	keyFields := strings.Fields(hostKey)
	if len(keyFields) < 2 || keyFields[0] != "ssh-ed25519" {
		t.Fatalf("invalid server-state host key: %q", hostKey)
	}
	// Follow ADR-0008: a recorded key alone precedes signature verification.
	// NEWKEYS proves the exchange completed; authentication is disabled. The client
	// logs to ssh.log (-E) because the Windows client blocks on an inherited pipe.
	deadline := time.Now().Add(15 * time.Second)
	var diagnostics []byte
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, "ssh", "-v", "-F", "none", "-E", "ssh.log", "-p", port,
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
		log, _ := os.ReadFile(filepath.Join(directory, "ssh.log"))
		diagnostics = append(diagnostics, log...)
		_ = os.Remove(filepath.Join(directory, "ssh.log"))
		known, readErr := os.ReadFile(filepath.Join(directory, "known_hosts"))
		fields := strings.Fields(string(known))
		if readErr == nil && len(fields) == 3 && fields[0] == "[127.0.0.1]:"+port &&
			fields[1] == keyFields[0] && fields[2] == keyFields[1] &&
			strings.Contains(string(diagnostics), "debug1: SSH2_MSG_NEWKEYS received") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no completed Ed25519 key exchange for %s on recorded port %s:\n%q", name, port, diagnostics)
}

// Bounds of the live host-key query. The manager waits up to six seconds for the
// entrypoint's ssh-keygen to publish the key, and podman exec start-up comes on
// top of that wait. The bounds match the CLI's first read-only manager attempt
// (a 10 s budget with 1 s of it kept for draining output), so the helper waits
// for the manager's own answer instead of killing the query first.
const (
	liveHostKeyTimeout   = 9 * time.Second
	liveHostKeyWaitDelay = time.Second
)

// sshHostKey exercises the same administrative query used by SSH setup immediately after startup
// and fails the test with readSSHHostKey's diagnostics when the query does not return a key.
func (host liveHost) sshHostKey(t *testing.T, name string) string {
	t.Helper()
	key, err := host.readSSHHostKey(name)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// readSSHHostKey runs the manager's ssh-host-key query in sandbox name within liveHostKeyTimeout,
// keeping liveHostKeyWaitDelay after the process ends or is killed to drain its output, and
// returns the single Ed25519 key line from stdout. A failure names the elapsed time, the process
// result, and the manager's stdout and stderr, so a manager error is not mistaken for a timeout.
func (host liveHost) readSSHHostKey(name string) (string, error) {
	args, err := cli.ManagerQueryArgs(cli.DerivedNames(host.group, name).Container, "/usr/local/bin/sandboxed-agents-manager", "ssh-host-key")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveHostKeyTimeout)
	defer cancel()
	command := host.podmanCommand(ctx, args...)
	command.WaitDelay = liveHostKeyWaitDelay
	var stdout, stderr bytes.Buffer
	started := time.Now()
	err = capture.Run(command, &stdout, &stderr)
	host.recordOperation("podman", "SSH server check", started, err)
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	key := strings.TrimSpace(stdout.String())
	if err == nil && strings.HasPrefix(key, "ssh-ed25519 ") && !strings.ContainsAny(key, "\r\n") {
		return key, nil
	}
	if err == nil {
		err = errors.New("manager printed no single Ed25519 public key")
	}
	host.observeFailure("podman", "SSH server check", err)
	return "", fmt.Errorf("read live SSH host key for %s after %s (deadline %s, output drain %s): %w\nmanager stdout: %q\nmanager stderr: %q",
		name, elapsed, liveHostKeyTimeout, liveHostKeyWaitDelay, err, stdout.String(), stderr.String())
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
			t.Fatalf("prepare embedded live executable (go %v): %v\n%q", args, err, output)
		}
	}
	return binary
}

// cli runs the live executable with the host environment and a bounded execution time.
func (host liveHost) cli(input string, args ...string) (string, error) {
	return host.cliContext(context.Background(), input, args...)
}

// cliContext supervises only the live invocation's host process tree. A deadline does not prove
// a remote engine operation stopped; report that ambiguity for the suite's existing reconciliation.
func (host liveHost) cliContext(ctx context.Context, input string, args ...string) (string, error) {
	operation := liveCLIClass(args)
	ctx, cancel := context.WithTimeout(ctx, host.operationBound(operation))
	defer cancel()
	started := time.Now()
	host.reportOperation(operation, "started")
	output := liveOutput{lines: liveLineStream{report: host.report}}
	var err error
	if len(args) > 0 && args[0] == "remove" && host.removals != nil {
		select {
		case host.removals <- struct{}{}:
		default:
			host.reportOperation(operation, "waiting for another suite remove")
			select {
			case host.removals <- struct{}{}:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		if err == nil {
			defer func() { <-host.removals }()
		}
	}
	var token string
	if err == nil {
		environment, issued := host.timingEnvironment(operation)
		token = issued
		command := exec.Command(host.binary, args...)
		command.Env = environment
		command.Stdin = strings.NewReader(input)
		command.WaitDelay = time.Second
		err = capture.RunOwned(ctx, command, &output, &output)
	}
	output.Flush()
	seconds := host.recordOperation("cli", operation, started, err)
	host.finishTiming(token, seconds, err)
	host.reportOperation(operation, fmt.Sprintf("finished after %s", time.Since(started).Round(time.Millisecond)))
	if err != nil {
		err = fmt.Errorf("live CLI failed after %s (operation %s, bound %s): %w", time.Since(started).Round(time.Millisecond), operation, host.operationBound(operation), err)
		if ctx.Err() != nil {
			err = fmt.Errorf("%w; host invocation cancelled; engine operations may still be running: reconcile recorded objects before cleanup", err)
		}
	}
	host.observeFailure("cli", operation, err)
	return output.String(), err
}

// mustCLI runs and logs a live CLI command, failing the test when the executable fails.
func (host liveHost) mustCLI(t *testing.T, input string, args ...string) string {
	t.Helper()
	t.Logf("sandboxed-agents %s", strings.Join(args, " "))
	output, err := host.cli(input, args...)
	if err != nil {
		t.Fatalf("sandboxed-agents %v: %v; output %q", args, err, output)
	}
	return output
}

// podman runs a bounded live Podman command against the selected connection.
func (host liveHost) podman(args ...string) (string, error) {
	return host.podmanOperation("inspection", args...)
}

// podmanOperation attributes a direct read to its caller's fixed operation class.
func (host liveHost) podmanOperation(operation string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), host.operationBound(operation))
	defer cancel()
	command := host.podmanCommand(ctx, args...)
	started := time.Now()
	host.reportOperation(operation, "started")
	output := liveOutput{lines: liveLineStream{report: host.report}}
	err := capture.Run(command, &output, &output)
	output.Flush()
	host.recordOperation("podman", operation, started, err)
	host.reportOperation(operation, fmt.Sprintf("finished after %s", time.Since(started).Round(time.Millisecond)))
	if ctx.Err() != nil {
		err = fmt.Errorf("live Podman operation %s ended after %s: %w; engine operations may still be running", operation, time.Since(started).Round(time.Millisecond), errors.Join(ctx.Err(), err))
	}
	if !expectedLiveAbsence(args, err) {
		host.observeFailure("podman", operation, err)
	}
	return output.String(), err
}

// podmanCommand creates a live Podman command bound to the selected connection and clears ambient Windows target overrides.
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

// selectConnection binds direct Windows Podman calls to the default or sole machine used by the CLI.
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
		t.Fatalf("select live Podman machine: %v\n%q", err, output)
	}
	chosen, defaults := machines[0].Name, 0
	for _, machine := range machines {
		if machine.Default {
			chosen = machine.Name
			defaults++
		}
	}
	if len(machines) > 1 && defaults != 1 {
		t.Fatal("the live suite requires the command-selected default or sole Podman machine")
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
		Image, User   string
		Labels        map[string]string
		CreateCommand []string
	}
	Mounts     []struct{ Type, Name, Source, Destination string }
	HostConfig struct {
		Memory, MemorySwap                      int64
		CPUQuota, CPUPeriod, PidsLimit, ShmSize int64
		PortBindings                            map[string][]struct{ HostIP, HostPort string }
		NetworkMode, UsernsMode                 string
		Privileged                              bool
		CapAdd, SecurityOpt, Binds              []string
		Devices                                 []json.RawMessage
	}
}

// assertNoSwap checks both Podman's recorded ceiling and the kernel's cgroup
// swap limit for a newly created sandbox or its replacement.
func (host liveHost) assertNoSwap(t *testing.T, name string, container liveContainer) {
	t.Helper()
	if container.HostConfig.Memory <= 0 || container.HostConfig.MemorySwap != container.HostConfig.Memory {
		t.Fatalf("sandbox %s does not record a no-swap memory limit: %#v", name, container.HostConfig)
	}
	output, err := host.podman("exec", "--user=1000:1000", cli.DerivedNames(host.group, name).Container, "cat", "/sys/fs/cgroup/memory.swap.max")
	if err != nil || strings.TrimSpace(output) != "0" {
		t.Fatalf("sandbox %s swap is not disabled in its cgroup: %v %q", name, err, output)
	}
}

// inspectContainer reads and validates the live sandbox container through Podman.
func (host liveHost) inspectContainer(t *testing.T, name string) liveContainer {
	t.Helper()
	output, err := host.podman("container", "inspect", cli.DerivedNames(host.group, name).Container)
	var records []liveContainer
	if err != nil || json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].Image == "" || records[0].Config.Image == "" {
		t.Fatalf("inspect live sandbox %s: %v\n%q", name, err, output)
	}
	return records[0]
}

// inspectImage reads and validates a live image record through Podman.
func (host liveHost) inspectImage(t *testing.T, tag string) liveImage {
	t.Helper()
	output, err := host.podman("image", "inspect", tag)
	var records []liveImage
	if err != nil || json.Unmarshal([]byte(output), &records) != nil || len(records) != 1 || records[0].ID == "" {
		t.Fatalf("inspect live image %s: %v\n%q", tag, err, output)
	}
	return records[0]
}

// sameImageID compares image IDs with or without the sha256 prefix.
func sameImageID(left, right string) bool {
	return strings.TrimPrefix(left, "sha256:") == strings.TrimPrefix(right, "sha256:")
}

// cleanupSandbox registers forced sandbox and volume removal followed by a Podman absence check.
func (host liveHost) cleanupSandbox(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		output, err := host.cli("", "remove", name, "--volumes", "--force")
		if err != nil && !strings.Contains(output, "does not exist in this controller group") {
			t.Errorf("cleanup sandbox %s: %v\n%q", name, err, output)
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
		output, err := host.podmanOperation("cleanup", resource.kind, "exists", resource.name)
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			continue
		}
		t.Errorf("left or could not verify %s %s: %v %q", resource.kind, resource.name, err, output)
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
		output, err := host.podmanOperation("cleanup", query...)
		if err != nil {
			t.Errorf("inventory of the controller group's objects: podman %v: %v\n%q", query, err, output)
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
		t.Errorf("objects of controller group %s remain: %q", host.group, leftovers)
	}
}

// liveListRow returns the sandbox's row of a list output and whether it is listed.
func liveListRow(listing, name string) (string, bool) {
	for _, row := range strings.Split(listing, "\n") {
		if fields := strings.Fields(row); len(fields) >= 2 && fields[0] == name {
			return row, true
		}
	}
	return "", false
}

// assertListed checks the sandbox's state in list, or its absence when state is empty.
func (host liveHost) assertListed(t *testing.T, name, state string) {
	t.Helper()
	listing := host.mustCLI(t, "", "list")
	row, listed := liveListRow(listing, name)
	switch {
	case listed && state == "":
		t.Fatalf("list still shows removed sandbox %s: %q", name, row)
	case listed && strings.Fields(row)[1] != state:
		t.Fatalf("list shows %s as %s; want %s: %q", name, strings.Fields(row)[1], state, row)
	case !listed && state != "":
		t.Fatalf("list omitted %s:\n%q", name, listing)
	}
}

// removeGroupStateOnCleanup registers removal of the live controller group's host state.
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

// liveOutput serializes the two capture streams while retaining their observed interleaving.
type liveOutput struct {
	sync.Mutex
	buffer bytes.Buffer
	lines  liveLineStream
}

// Write appends one captured write atomically with respect to the other stream.
func (b *liveOutput) Write(data []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	b.lines.Write(data)
	return b.buffer.Write(data)
}

// Flush emits the final partial payload line after both process streams have drained.
func (b *liveOutput) Flush() { b.Lock(); defer b.Unlock(); b.lines.Flush() }

// String returns the completed combined trace without racing a stream writer.
func (b *liveOutput) String() string { b.Lock(); defer b.Unlock(); return b.buffer.String() }

// liveLineStream separates escaped process payload from host operation metadata.
// Its owner serializes writes; pending memory is bounded even without a newline.
type liveLineStream struct {
	report  func(string)
	pending []byte
}

// Write streams whole lines or 4096-byte fragments without exposing payload newlines.
func (lines *liveLineStream) Write(data []byte) {
	if lines.report == nil {
		return
	}
	for len(data) > 0 {
		n := min(len(data), 4096-len(lines.pending))
		if end := bytes.IndexByte(data[:n], '\n'); end >= 0 {
			n = end + 1
		}
		lines.pending = append(lines.pending, data[:n]...)
		data = data[n:]
		if len(lines.pending) == 4096 || lines.pending[len(lines.pending)-1] == '\n' {
			lines.Flush()
		}
	}
}

// Flush publishes a bounded, ASCII-escaped payload record, including a final partial line.
func (lines *liveLineStream) Flush() {
	if len(lines.pending) > 0 && lines.report != nil {
		lines.report("live output: " + strconv.QuoteToASCII(string(lines.pending)))
		lines.pending = lines.pending[:0]
	}
}
