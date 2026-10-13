// Package cli owns command grammar and the host command entry point.
package cli

import (
	"fmt"
	"io"
	"os"

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

// RunWithHost runs the public command boundary with read-only host facts supplied
// by the caller. Zero-valued fields use the real host; processes still run on PATH.
func RunWithHost(args []string, stdout, stderr io.Writer, host Host) int {
	host = host.defaults()
	var stateRoot string
	var request Request
	err := checks.Run(checks.Checks{Usage: func() error {
		group, set := os.LookupEnv("SANDBOXED_AGENTS_GROUP")
		if !set {
			group = "default"
		}
		var err error
		request, err = Parse(args, group)
		if err == nil {
			switch request.Command {
			case "up", "start", "stop", "restart", "remove":
				stateRoot, err = hostStateRoot(host.Platform)
			}
		}
		return err
	}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "usage: sandboxed-agents COMMAND [ARG...]")
		return 2
	}
	checkedTarget := func() (podmanTarget, error) {
		target, err := preflight(host.defaults())
		if err != nil {
			fmt.Fprintln(stderr, "step 2:", err)
		}
		return target, err
	}
	switch request.Command {
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
			fmt.Fprintln(stderr, "check NAME: sandbox checks are not implemented")
			return 1
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
		tag, err := buildImage(target, request.Toolchains, stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "built image", tag)
		return 0
	case "up":
		target, err := checkedTarget()
		if err != nil {
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
	case "start", "stop", "restart", "remove":
		target := podmanTarget{host: host, environment: os.Environ}
		if host.Platform == "windows" {
			target.environment = windowsPodmanEnvironment
			var err error
			target, err = selectWindowsTarget(target)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
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
		return 0

	}
	fmt.Fprintf(stderr, "%s: not implemented\n", request.Command)
	return 1
}
