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
	"strconv"
	"strings"
	"time"
)

// CleanupOptions selects retained invocation evidence and optional exact image IDs to keep.
type CleanupOptions struct {
	RunDir string
	Retain []string
}

// cleanupRetention collects repeatable full-ID retention arguments.
type cleanupRetention []string

// String formats the flag's existing retention values.
func (r *cleanupRetention) String() string { return strings.Join(*r, ",") }

// Set admits one full immutable image ID, never a tag, prefix or option.
func (r *cleanupRetention) Set(value string) error {
	if !fullImageID(value) {
		return fmt.Errorf("retain requires a full image ID")
	}
	*r = append(*r, canonicalImageID(value))
	return nil
}

// ParseCleanupArgs validates standalone cleanup arguments without accessing an engine.
func ParseCleanupArgs(args []string, diagnostics io.Writer) (CleanupOptions, error) {
	var o CleanupOptions
	var retain cleanupRetention
	flags := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.StringVar(&o.RunDir, "run-dir", "", "private directory of the completed invocation")
	flags.Var(&retain, "retain", "full image ID to keep (repeatable)")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if flags.NArg() != 0 || o.RunDir == "" {
		return o, fmt.Errorf("supply -run-dir")
	}
	var err error
	o.RunDir, err = filepath.Abs(o.RunDir)
	o.Retain = []string(retain)
	return o, err
}

// cleanupRun retains private inputs and the output of one cleanup attempt.
type cleanupRun struct {
	options    Options
	before     Snapshot
	ended      exitEvidence
	started    startedRun
	directory  string
	queries    int
	report     CleanupReport
	containers []Container
}

// Cleanup applies the invocation's image eligibility rules and publishes sanitized evidence.
// It never starts the suite; the caller must dispatch this method after suite exit is recorded.
func (s *Service) Cleanup(ctx context.Context, options CleanupOptions, out io.Writer) error {
	run, err := loadCleanup(options)
	if err != nil {
		return err
	}
	lock, err := acquireEngine(run.options.LockRoot, run.before.EngineID)
	if err != nil {
		return fmt.Errorf("cleanup: pending (engine exclusion unavailable)")
	}
	defer lock.Close()
	if err = refuseActive(run.options, run.before.EngineID); err != nil {
		return fmt.Errorf("cleanup: pending (owned suite may still be running)")
	}
	if err = s.cleanupCheckout(ctx, run); err != nil {
		return err
	}
	target, engine, err := s.target(ctx, run.options)
	if err != nil {
		_ = cleanupWriteJSON(filepath.Join(run.directory, "engine-diagnostic.json"), map[string]string{"error": boundedCleanupText(err.Error(), 4096)}, cleanupPrivateLimit)
		return errCleanupUnreachable
	}
	if engine != run.before.EngineID || target != run.before.Target {
		return fmt.Errorf("cleanup: pending (engine changed since before snapshot)")
	}
	log, err := cleanupRead(filepath.Join(run.options.RunDir, "suite.log"), cleanupLogLimit)
	if err != nil {
		return fmt.Errorf("cleanup: pending (private log unreadable or exceeds limit)")
	}
	proofs, asset, err := parseBuildOutput(log)
	if err != nil {
		return fmt.Errorf("cleanup: pending (build evidence incomplete or exceeds limit)")
	}
	current, issues, err := s.cleanupInventory(ctx, run, target)
	run.report.Unverified = append(run.report.Unverified, issues...)
	if errors.Is(err, errCleanupUnreachable) {
		return errCleanupUnreachable
	}
	if err != nil {
		run.report.Status = "unverified"
		for _, image := range current {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: image.ID, Tags: cleanupTags(image.RepoTags), SizeBytes: image.Size, SizeVerified: image.Verified, Reason: "whole-engine references unverified; no removal attempted", Query: "podman ps --all / container inspect"})
		}
		return s.finishCleanup(ctx, run, log, asset, out, err)
	}
	record := resolveBuildRecord(proofs, current)
	if err = cleanupWriteJSON(filepath.Join(run.directory, "build-record.json"), record, cleanupPrivateLimit); err != nil {
		return fmt.Errorf("cleanup: pending (cannot retain build record)")
	}
	for _, entry := range record {
		if entry.Reason != "" {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: entry.Token, Reason: entry.Reason, Query: "build output / full inventory / image inspect"})
		}
	}
	plan := cleanupPlan(run.before, current, record, asset, options.Retain)
	if err = cleanupWriteJSON(filepath.Join(run.directory, "removal-plan.json"), plan, cleanupSummaryLimit/2); err != nil {
		return fmt.Errorf("cleanup: pending (cannot retain removal plan)")
	}
	for index, item := range plan {
		if item.Reason != "" {
			run.report.Skipped = append(run.report.Skipped, item)
			continue
		}
		// Recheck the endpoint and every container immediately before each removal.
		if err = s.cleanupEngine(ctx, run, target); err != nil {
			if errors.Is(err, errCleanupUnreachable) && len(run.report.Removed) == 0 {
				return errCleanupUnreachable
			}
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "engine identity unverified", Query: "podman info --format json"})
			run.skipRemainder(plan[index+1:])
			break
		}
		used, queryErr := s.cleanupReferences(ctx, run, target)
		if queryErr != nil {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "container references unverified", Query: "podman ps --all / container inspect"})
			run.skipRemainder(plan[index+1:])
			break
		}
		if used[item.ID] {
			item.Reason = "referenced by a container (any controller group)"
			run.report.Skipped = append(run.report.Skipped, item)
			continue
		}
		if err = s.cleanupCheckout(ctx, run); err != nil {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "checkout changed or unverified", Query: "git revision / status"})
			run.skipRemainder(plan[index+1:])
			break
		}
		if _, err = s.cleanupQuery(ctx, run, target, "rmi", "--no-prune", item.ID); err != nil {
			item.Reason = "removal failed; retained or unverified"
			item.Query = "podman rmi --no-prune ID"
			run.report.Unverified = append(run.report.Unverified, item)
			continue
		}
		run.report.Removed = append(run.report.Removed, item)
	}
	// Reconcile the final whole-engine state independently of successful removal output.
	final, finalIssues, finalErr := s.cleanupInventory(ctx, run, target)
	run.report.Unverified = append(run.report.Unverified, finalIssues...)
	if finalErr == nil {
		err = cleanupWriteJSON(filepath.Join(run.directory, "final-inventory.json"), struct {
			Images     []cleanupImage
			Containers []Container
			Complete   bool
		}{final, run.containers, len(finalIssues) == 0}, cleanupPrivateLimit)
		if err != nil {
			return fmt.Errorf("cleanup: pending (cannot retain final inventory)")
		}
	}

	if finalErr == nil {
		remaining := map[string]bool{}
		for _, image := range final {
			remaining[image.ID] = true
		}
		for _, item := range run.report.Removed {
			if remaining[item.ID] {
				run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "removal output disagrees with final inventory", Query: "podman images --all --no-trunc"})
			}
		}
		run.report.RemainingContainers = len(run.containers)
	}
	return s.finishCleanup(ctx, run, log, asset, out, finalErr)
}

// loadCleanup verifies complete, ordered invocation artifacts before any engine action.
func loadCleanup(o CleanupOptions) (*cleanupRun, error) {
	directory, err := filepath.Abs(o.RunDir)
	if err != nil || o.RunDir == "" {
		return nil, fmt.Errorf("supply -run-dir")
	}
	r := &cleanupRun{}
	for name, value := range map[string]any{"options.json": &r.options, "before.json": &r.before, "started.json": &r.started, "exit.json": &r.ended} {
		data, e := cleanupRead(filepath.Join(directory, name), cleanupPrivateLimit)
		if e != nil || json.Unmarshal(data, value) != nil {
			return nil, fmt.Errorf("cleanup: pending (invocation artifacts unreadable or incomplete)")
		}
		if name == "exit.json" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(data, &fields)
			for _, field := range []string{"Code", "EndedAt", "Interrupted"} {
				if _, ok := fields[field]; !ok {
					return nil, fmt.Errorf("cleanup: pending (suite exit evidence incomplete)")
				}
			}
		}
		if name == "before.json" {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(data, &fields)
			for _, field := range []string{"Images", "Containers", "Disks", "SystemDF", "TakenAt", "Target", "EngineID"} {
				if _, ok := fields[field]; !ok {
					return nil, fmt.Errorf("cleanup: pending (incomplete before snapshot)")
				}
			}
		}
	}
	before := r.before
	if !before.Complete || before.EngineID == "" || before.Target.Storage == "" || before.TakenAt.IsZero() || len(before.Disks) == 0 || before.SystemDF == "" {
		return nil, fmt.Errorf("cleanup: pending (incomplete before snapshot)")
	}
	seen := map[string]bool{}
	for _, image := range before.Images {
		id := canonicalImageID(image.ID)
		if !fullImageID(id) || seen[id] || image.Created.IsZero() {
			return nil, fmt.Errorf("cleanup: pending (invalid before image inventory)")
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	for _, container := range before.Containers {
		if !validName(container.ID) || seen[container.ID] || !fullImageID(container.Image) {
			return nil, fmt.Errorf("cleanup: pending (invalid before container inventory)")
		}
		seen[container.ID] = true
	}
	if r.options.RunDir != directory || !filepath.IsAbs(r.options.Checkout) || !filepath.IsAbs(r.options.RecordDir) || !filepath.IsAbs(r.options.LockRoot) || within(directory, r.options.RecordDir) || within(r.options.RecordDir, directory) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(r.options.Revision) {
		return nil, fmt.Errorf("cleanup: pending (invocation paths or revision changed)")
	}
	if r.ended.EndedAt.IsZero() || r.started.PreparationStart.IsZero() || r.started.PreparationEnd.Before(r.started.PreparationStart) || r.ended.EndedAt.Before(r.started.PreparationEnd) || r.before.TakenAt.After(r.started.PreparationEnd) || r.ended.EndedAt.After(time.Now().UTC()) {
		return nil, fmt.Errorf("cleanup: pending (suite exit or preparation interval unverified)")
	}
	status, err := cleanupRead(filepath.Join(directory, "exit-status"), 32)
	if err != nil || strings.TrimSpace(string(status)) != strconv.Itoa(r.ended.Code) {
		return nil, fmt.Errorf("cleanup: pending (suite exit status unverified)")
	}
	for _, id := range o.Retain {
		if !fullImageID(id) {
			return nil, fmt.Errorf("retain requires a full image ID")
		}
	}
	r.directory, err = os.MkdirTemp(directory, "cleanup-")
	if err != nil {
		return nil, fmt.Errorf("cleanup: pending (cannot retain private evidence)")
	}
	r.report = CleanupReport{Status: "complete", SuiteExitCode: r.ended.Code, Interrupted: r.ended.Interrupted, Before: cleanupMeasurement(before.Disks, before.SystemDF)}
	return r, nil
}

// cleanupCheckout refuses a changed revision or modified tracked checkout through read-only queries.
func (s *Service) cleanupCheckout(ctx context.Context, r *cleanupRun) error {
	data, err := s.query(ctx, r.options, "git", "rev-parse", "--verify", "HEAD^{commit}")
	if err == nil && strings.TrimSpace(string(data)) == r.options.Revision {
		data, err = s.query(ctx, r.options, "git", "status", "--porcelain", "--untracked-files=no")
		if err == nil && len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
	}
	_ = cleanupWriteJSON(filepath.Join(r.directory, "checkout-diagnostic.json"), map[string]string{"error": boundedCleanupText(fmt.Sprint(err), 4096)}, cleanupPrivateLimit)
	return fmt.Errorf("cleanup: pending (checkout changed or unverified)")
}

// skipRemainder accounts for every unattempted image after a destructive-action guard fails.
func (r *cleanupRun) skipRemainder(items []CleanupItem) {
	for _, item := range items {
		if item.Reason == "" {
			item.Reason = "not attempted after failed revalidation"
		}
		r.report.Skipped = append(r.report.Skipped, item)
	}
}
