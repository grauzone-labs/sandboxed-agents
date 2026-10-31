package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sandboxCreateProgressAfter is how long a sandbox create runs before the CLI writes one line
// saying that the create is still running. It only reports; it never ends the create.
var sandboxCreateProgressAfter = 2 * time.Minute

// sandboxCreateArgs keeps the isolation, identity, and storage policy shared by up and update.
func sandboxCreateArgs(request Request, port uint16, workspaceMount, tag string, extraLabels ...string) []string {
	names := DerivedNames(request.Group, request.Name)
	args := append([]string{"create", "--name", names.Container}, sandboxIdentityLabels(request)...)
	args = append(args, "--label", SSHPortLabel+"="+strconv.Itoa(int(port)),
		"--userns=keep-id:uid=1000,gid=1000", "--user=0:0", "--security-opt=no-new-privileges", "--network=pasta:--no-map-gw",
		"--memory", strconv.FormatInt(request.Resources.Memory, 10),
		"--memory-swap", strconv.FormatInt(request.Resources.MemorySwap, 10), "--cpus", strconv.FormatFloat(request.Resources.CPUs, 'f', -1, 64),
		"--pids-limit", strconv.FormatInt(request.Resources.PIDsLimit, 10), "--shm-size", strconv.FormatInt(request.Resources.ShmSize, 10),
		"--publish", fmt.Sprintf("127.0.0.1:%d:22/tcp", port),
		"--label", ToolchainsLabel+"="+toolchainSet(request.Toolchains),
		"--mount", workspaceMount,
		"--mount", "type=volume,source="+names.Home+",destination=/home/agent",
		"--mount", "type=volume,source="+names.SSH+",destination=/etc/ssh")
	for _, label := range extraLabels {
		args = append(args, "--label", label)
	}
	return append(args, tag)
}

// sandboxIdentityLabels returns the owner and sandbox-name label arguments for Podman objects.
func sandboxIdentityLabels(request Request) []string {
	return []string{"--label", OwnerLabel + "=" + request.Group, "--label", SandboxNameLabel + "=" + request.Name}
}

// sandboxCreate names one Podman create of a sandbox container for its progress line and its
// timeout error.
type sandboxCreate struct {
	sandbox, container, image string
	// check is the Podman command line a user runs to look for the container.
	check string
	// progressAfter is the run time after which the progress line is written once.
	progressAfter time.Duration
	progress      io.Writer
	now           func() time.Time
}

// createSandboxContainer runs the sandbox's podman create arguments without a time bound. When
// the create runs past sandboxCreateProgressAfter it writes one progress line to progress and
// keeps waiting; a create ended by a timeout or signal returns an error naming the create step,
// the elapsed time, and the sandbox.
func createSandboxContainer(p podmanTarget, request Request, args []string, progress io.Writer) error {
	container := DerivedNames(request.Group, request.Name).Container
	create := sandboxCreate{
		sandbox:       request.Name,
		container:     container,
		image:         args[len(args)-1],
		check:         "podman " + strings.Join(p.arguments([]string{"ps", "--all", "--filter", "name=" + container}), " "),
		progressAfter: sandboxCreateProgressAfter,
		progress:      progress,
		now:           time.Now,
	}
	return create.run(context.Background(), func(ctx context.Context) error {
		_, err := p.queryContext(ctx, 0, args...)
		return err
	})
}

// run calls create with ctx, writes the progress line once if create is still running after
// progressAfter, and never writes it after run returns. A create that ctx ended or a signal
// terminated returns the timeout error; every other error is returned unchanged.
func (c sandboxCreate) run(ctx context.Context, create func(context.Context) error) error {
	started := c.now()
	var mu sync.Mutex
	finished := false
	timer := time.AfterFunc(c.progressAfter, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			fmt.Fprintln(c.progress, c.progressLine(c.now().Sub(started)))
		}
	})
	err := create(ctx)
	timer.Stop()
	mu.Lock()
	finished = true
	mu.Unlock()
	if err != nil && (ctx.Err() != nil || terminatedBySignal(err)) {
		return fmt.Errorf("sandbox %s: create step (podman create of container %s from image %s) ended after %s without finishing: %w",
			c.sandbox, c.container, c.image, roundElapsed(c.now().Sub(started)), err)
	}
	return err
}

// progressLine returns the single line that reports a sandbox create still running after elapsed.
func (c sandboxCreate) progressLine(elapsed time.Duration) string {
	return fmt.Sprintf("sandbox %s: podman create of container %s is still running after %s; a first create of a new image can take several minutes while Podman prepares the user-namespace layer copy; the create continues; check it with: %s (image %s)",
		c.sandbox, c.container, roundElapsed(elapsed), c.check, c.image)
}

// terminatedBySignal reports whether err carries a process exit that a signal caused rather than
// an exit status.
func terminatedBySignal(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() < 0
}

// roundElapsed rounds a run time to whole seconds, or to milliseconds below one second.
func roundElapsed(elapsed time.Duration) time.Duration {
	if elapsed < time.Second {
		return elapsed.Round(time.Millisecond)
	}
	return elapsed.Round(time.Second)
}
