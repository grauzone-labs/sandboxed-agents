// Command retain-durations derives supplemental evidence from one verbose live-suite log on stdin.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/grauzone-labs/sandboxed-agents/internal/validationduration"
)

// main runs the offline collector without executing the live suite or any host command.
func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run collects a single run's stdin log beside its existing schema 2 record.
func run(args []string, input io.Reader, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("retain-durations", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	record := flags.String("record", "", "existing live-suite-<platform>.json from this run")
	host := flags.String("host-load", "unknown", "observed host load before the run: idle, shared, unknown")
	base := flags.String("base-image", "unknown", "base image before the run: present, absent, unknown")
	cache := flags.String("build-cache", "unknown", "build cache before the run: warm, cold, unknown")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *record == "" || flags.NArg() != 0 {
		return fmt.Errorf("supply -record and one verbose suite log on stdin")
	}
	path, err := validationduration.Retain(input, *record, validationduration.Context{HostLoad: *host, BaseImage: *base, BuildCache: *cache})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, path)
	return err
}
