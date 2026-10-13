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
	var request Request
	err := checks.Run(checks.Checks{Usage: func() error {
		group, set := os.LookupEnv("SANDBOXED_AGENTS_GROUP")
		if !set {
			group = "default"
		}
		var err error
		request, err = Parse(args, group)
		return err
	}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "usage: sandboxed-agents COMMAND [ARG...]")
		return 2
	}
	if request.Command == "version" {
		hash, err := image.Hash(image.Context())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "sandboxed-agents", version)
		fmt.Fprintln(stdout, "asset-sha256", hash)
		return 0
	}
	var target podmanTarget
	if request.Command == "check" || request.Command == "build" || request.Command == "up" {
		target, err = preflight(host.defaults())
		if err != nil {
			fmt.Fprintln(stderr, "step 2:", err)
			return 1
		}
		if request.Command == "check" {
			if request.Name != "" {
				fmt.Fprintln(stderr, "check NAME: sandbox checks are not implemented")
				return 1
			}
			fmt.Fprintln(stdout, "host prerequisites satisfied")
			return 0
		}

	}
	if request.Command == "build" {
		tag, err := buildImage(target, stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "built image", tag)
		return 0
	}
	if request.Command == "up" {
		if err := prepareUp(request, target, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "%s: not implemented\n", request.Command)
	return 1
}
