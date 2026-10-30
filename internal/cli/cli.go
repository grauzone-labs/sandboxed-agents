// Package cli owns command grammar and the host command entry point.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/image"
	"github.com/grauzone-labs/sandboxed-agents/internal/checks"
)

// version is overridden with -ldflags -X for release builds.
var version = "development"

// Run returns zero only when the requested operation succeeds. Commands whose
// lifecycle work is deferred cannot silently claim sandbox creation or reuse.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithHost(args, stdout, stderr, Host{})
}

// RunWithHost runs the public command boundary with host facts and shell input
// supplied by the caller. Zero-valued fields use the real host; processes run on PATH.
// A help request prints its help to stdout and exits 0 without Podman or host state;
// a usage error exits 2 with the usage of the deepest level the arguments reached, and a
// host state root that cannot be resolved exits 2 with the generic usage line, since help
// cannot fix it.
func RunWithHost(args []string, stdout, stderr io.Writer, host Host) int {
	host = host.defaults()
	var stateRoot string
	var request Request
	var topic string
	var help bool
	err := checks.Run(checks.Checks{Usage: func() error {
		var err error
		if topic, help, err = requestedHelp(args); help {
			return err
		}
		group, set := os.LookupEnv("SANDBOXED_AGENTS_GROUP")
		if !set {
			group = "default"
		}
		request, err = parseForHost(args, group, host)
		topic = usageTopic(request.Command)
		if err == nil {
			switch request.Command {
			case "up", "start", "stop", "restart", "remove", "update":
				stateRoot, err = hostStateRoot(host.Platform)
			case "ssh-config":
				if request.Options["install"] != "" || request.Options["remove"] != "" {
					stateRoot, err = hostStateRoot(host.Platform)
				}
			}
		}
		return err
	}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.As(err, new(hostStateError)) {
			fmt.Fprintln(stderr, "usage: sandboxed-agents COMMAND [ARG...]")
		} else {
			writeUsage(stderr, topic)
		}
		return 2
	}
	if help {
		writeHelp(stdout, topic)
		return 0
	}
	checkedTarget := func() (podmanTarget, error) {
		target, err := preflight(host)
		if err != nil {
			fmt.Fprintln(stderr, "step 2:", err)
		}
		return target, err
	}
	selectedTarget := func() (podmanTarget, error) {
		target, err := selectTarget(host)
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		return target, err
	}
	switch request.Command {
	case "list":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := listSandboxes(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "version":
		hash, err := image.Hash(image.Context())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "sandboxed-agents", version)
		fmt.Fprintln(stdout, "asset-sha256", hash)
		return 0
	case "check":
		if request.Name != "" {
			target, err := selectedTarget()
			if err != nil {
				return 1
			}
			if err := checkSandbox(request, target, stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		target, err := checkedTarget()
		if err != nil {
			return 1
		}
		fmt.Fprintln(stdout, "host prerequisites satisfied")
		if target.automountRoot != "" {
			fmt.Fprintf(stdout, "machine %s automount root %s\n", target.connection, target.automountRoot)
		}
		return 0
	case "build":
		target, err := checkedTarget()
		if err != nil {
			return 1
		}
		err = rebuildImages(request, target, stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "up":
		target, err := checkedTarget()
		if err != nil {
			return 1
		}
		if err := checkWorkspaceAutomount(request, target); err != nil {
			fmt.Fprintln(stderr, "step 2:", err)
			return 1
		}
		lock, err := acquireLifecycleLock(stateRoot, request)
		if err != nil {
			fmt.Fprintln(stderr, "step 3:", err)
			return 1
		}
		defer lock.Close()
		if err := prepareUp(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := enableUpAgents(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := restoreToolServices(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "start", "stop", "restart", "remove":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		lock, err := acquireLifecycleLock(stateRoot, request)
		if err != nil {
			fmt.Fprintln(stderr, "step 3:", err)
			return 1
		}
		defer lock.Close()
		if err := changeSandboxState(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if request.Command == "start" || request.Command == "restart" {
			if err := restoreToolServices(request, target, stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		return 0
	case "update":
		target, err := checkedTarget()
		if err != nil {
			return 1
		}
		if request.Name != "" {
			lock, err := acquireLifecycleLock(stateRoot, request)
			if err != nil {
				fmt.Fprintln(stderr, "step 3:", err)
				return 1
			}
			defer lock.Close()
			if err := updateSandbox(request, target, stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		if err := updateAllSandboxes(request, target, stateRoot, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "shell":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		interactive := host.IsInputTerminal(host.Stdin) && host.IsOutputTerminal(stdout)
		status, err := openShell(request, target, interactive, stdout, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		return status
	case "integrations config", "integrations login":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := runIntegration(request, target, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "agents enable", "agents disable", "agents update", "agents status":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := administerAgent(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "agents run", "agents login", "agents session":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		status, err := runAgentWork(request, target, stdout, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		return status
	case "tools enable", "tools update", "tools status", "tools delete":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := administerTool(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "tools connect":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := connectTool(request, target, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "tools tailscale", "tools pair":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := runToolTerminalWork(request, target, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0

	case "fingerprint":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if err := printFingerprints(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "ssh-config":
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		if request.Options["install"] != "" || request.Options["remove"] != "" {
			lock, err := acquireLifecycleLock(stateRoot, request)
			if err != nil {
				fmt.Fprintln(stderr, "step 3:", err)
				return 1
			}
			defer lock.Close()
		}
		if err := configureSSH(request, target, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0

	}
	if request.Name != "" {
		target, err := selectedTarget()
		if err != nil {
			return 1
		}
		objects, err := requireSandbox(request, target)
		if err == nil && (strings.HasPrefix(request.Command, "agents ") || strings.HasPrefix(request.Command, "integrations ") || (request.Command == "ssh-config" && request.Options["install"] != "")) {
			_, err = requireRunning(request, objects)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "%s: not implemented\n", request.Command)
	return 1
}
