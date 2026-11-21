package liverun

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// revisionLine admits only a complete immutable commit identity.
var revisionLine = regexp.MustCompile("^[0-9a-f]{40}$")

// Options records the host inputs needed to repeat preparation under the engine lock.
type Options struct {
	Checkout          string
	RunDir            string
	RecordDir         string
	LockRoot          string
	Revision          string
	PeakRecord        string
	Group             string
	Images            bool
	Overlap           int
	BuildJobs         int
	HostLoad          string
	AllowUnbounded    bool
	UnboundedDecision string
	PreparationStart  time.Time
}

// RunDispatch implements the public phase command interface.
func (s *Service) RunDispatch(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("supply prepare, run, status, collect, or cleanup")
	}
	switch args[0] {
	case "prepare":
		options, err := s.parseOptions(args[1:], diagnostics)
		if err != nil {
			return err
		}
		_, err = s.prepare(ctx, options)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(struct{ Prepared bool }{true})
	case "run":
		return s.launch(ctx, args[1:], out, diagnostics)
	case "status":
		return s.status(ctx, args[1:], out, diagnostics)
	case "collect":
		return s.collect(ctx, args[1:], out, diagnostics)
	case "cleanup":
		options, err := ParseCleanupArgs(args[1:], diagnostics)
		if err != nil {
			return err
		}
		return s.Cleanup(ctx, options, out)
	case "_supervise":
		return s.supervise(ctx, args[1:])
	default:
		return fmt.Errorf("unknown live-run command")
	}
}

// parseOptions validates command inputs without starting any process.
func (s *Service) parseOptions(args []string, diagnostics io.Writer) (Options, error) {
	var o Options
	set := flag.NewFlagSet("prepare", flag.ContinueOnError)
	set.SetOutput(diagnostics)
	set.StringVar(&o.Checkout, "checkout", ".", "checkout under test")
	set.StringVar(&o.RunDir, "run-dir", "", "private run directory outside the record directory")
	set.StringVar(&o.RecordDir, "record-dir", "", "schema 2 records for this invocation")
	set.StringVar(&o.Revision, "revision", "", "full target commit")
	set.StringVar(&o.PeakRecord, "peak-record", "", "maintainer-authored conservative bound record; defaults to engine-specific KEY-peak.json")
	set.StringVar(&o.Group, "group", "live", "dedicated controller group")
	set.StringVar(&o.HostLoad, "host-load", "unknown", "observed other work before run: idle, shared, unknown")
	set.IntVar(&o.Overlap, "overlap", 1, "maximum independent suite work")
	set.IntVar(&o.BuildJobs, "build-jobs", 1, "parallel jobs in the suite rebuild")
	set.BoolVar(&o.Images, "images", true, "include documented image coverage")
	set.BoolVar(&o.AllowUnbounded, "allow-unbounded", false, "invocation-specific user decision to proceed without a bound")
	set.StringVar(&o.UnboundedDecision, "unbounded-decision", "", "record of that invocation-specific user decision")
	if err := set.Parse(args); err != nil {
		return o, err
	}
	if o.Overlap < 1 || o.BuildJobs < 1 {
		return o, fmt.Errorf("overlap and build-jobs must be positive integers")
	}
	if set.NArg() != 0 || !revisionLine.MatchString(o.Revision) || o.RunDir == "" || o.RecordDir == "" {
		return o, fmt.Errorf("supply full -revision, -run-dir and -record-dir")
	}
	if o.HostLoad != "idle" && o.HostLoad != "shared" && o.HostLoad != "unknown" {
		return o, fmt.Errorf("host-load must be idle, shared, or unknown")
	}
	if !regexp.MustCompile("^[a-z][a-z0-9-]{0,30}$").MatchString(o.Group) {
		return o, fmt.Errorf("invalid dedicated controller group")
	}
	if o.AllowUnbounded && strings.TrimSpace(o.UnboundedDecision) == "" {
		return o, fmt.Errorf("unbounded headroom requires the invocation-specific user decision")
	}
	for _, p := range []*string{&o.Checkout, &o.RunDir, &o.RecordDir} {
		v, err := filepath.Abs(*p)
		if err != nil {
			return o, err
		}
		*p = v
	}
	var err error
	o.LockRoot, err = s.coordinationRoot()
	if err != nil {
		return o, err
	}
	if within(o.Checkout, o.RunDir) || within(o.Checkout, o.RecordDir) {
		return o, fmt.Errorf("private run and record directories must be outside checkout")
	}
	if within(o.RecordDir, o.RunDir) || within(o.RunDir, o.RecordDir) {
		return o, fmt.Errorf("private run and record directories must be separate")
	}
	if o.PeakRecord != "" {
		o.PeakRecord, err = filepath.Abs(o.PeakRecord)
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// within reports whether path is at or below directory.
func within(directory, path string) bool {
	rel, err := filepath.Rel(directory, path)
	return err == nil && (rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."))
}

// query runs one read-only process within a short bound and keeps diagnostics private.
func (s *Service) query(ctx context.Context, o Options, program string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := s.Executor.Execute(bounded, Invocation{Program: program, Args: args, Dir: o.Checkout, Env: hostEnvironment(s.Platform, nil)})
	if err != nil {
		return nil, fmt.Errorf("%s query failed: %w; output: %s", program, err, data)
	}
	return data, nil
}

// failCheck saves engine diagnostics privately and returns only the failed check name and whether the save succeeded.
func failCheck(o Options, name string, err error) error {
	retained := "private diagnostic retained"
	if os.WriteFile(filepath.Join(o.RunDir, "diagnostic.txt"), []byte(fmt.Sprint(err)+"\n"), 0600) != nil {
		retained = "private diagnostic could not be retained"
	}
	var fault *engineFailure
	if name == "engine" && errors.As(err, &fault) {
		return fmt.Errorf("check engine failed; %s: %s; %s", fault.kind, fault.reason, retained)
	}
	return fmt.Errorf("check %s failed; %s", name, retained)
}

// normalizeConcurrency supplies serial defaults for legacy private options and
// refuses negative saved inputs before preparation or launch can query an engine.
func (o *Options) normalizeConcurrency() error {
	if o.Overlap < 0 || o.BuildJobs < 0 {
		return fmt.Errorf("invalid saved suite concurrency")
	}
	if o.Overlap == 0 {
		o.Overlap = 1
	}
	if o.BuildJobs == 0 {
		o.BuildJobs = 1
	}
	return nil
}
