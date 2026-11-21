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
	"sort"
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
	published  bool
}

// Cleanup applies the invocation's image eligibility rules and publishes sanitized evidence.
// It never starts the suite; the caller must dispatch this method after suite exit is recorded.
func (s *Service) Cleanup(ctx context.Context, options CleanupOptions, out io.Writer) error {
	run, err := loadCleanup(options)
	if err != nil {
		return err
	}
	if err = s.checkCoordination(run.options); err != nil {
		return fmt.Errorf("cleanup: pending (coordination root changed)")
	}
	lock, err := acquireEngine(run.options.LockRoot, run.before.EngineID)
	if err != nil {
		return fmt.Errorf("cleanup: pending (engine exclusion unavailable)")
	}
	defer lock.Close()
	// Deferred after the lock so completion evidence is published before a serialized rerun can take the lock.
	// Completion needs a published report, which only a run that took the lock can reach.
	defer retainCleanupCompletion(run)
	if err = refuseActive(run.options, run.before.EngineID); err != nil {
		return fmt.Errorf("cleanup: pending (owned suite may still be running)")
	}
	if err = s.cleanupCheckout(ctx, run); err != nil {
		return err
	}
	target, engine, err := s.target(ctx, run.options)
	if err != nil {
		_ = cleanupWriteJSON(filepath.Join(run.directory, "engine-diagnostic.json"), map[string]string{"error": boundedCleanupText(err.Error(), 4096)}, cleanupPrivateLimit)
		return cleanupPendingEngine(err)
	}
	if engine != run.before.EngineID || target != run.before.Target {
		return fmt.Errorf("cleanup: pending (engine changed since before snapshot)")
	}
	log, err := cleanupRead(filepath.Join(run.options.RunDir, "suite.log"), cleanupLogLimit)
	if err != nil {
		return fmt.Errorf("cleanup: pending (private log unreadable or exceeds limit)")
	}
	asset, err := parseBuildAsset(log)
	if err != nil {
		return fmt.Errorf("cleanup: pending (build evidence incomplete or exceeds limit)")
	}
	current, issues, err := s.cleanupInventory(ctx, run, target)
	run.report.Unverified = append(run.report.Unverified, issues...)
	if errors.Is(err, errCleanupUnreachable) {
		return err
	}
	if err != nil {
		run.report.Status = "unverified"
		for _, image := range current {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: image.ID, Tags: cleanupTags(image.RepoTags), SizeBytes: image.Size, SizeVerified: image.Verified, Reason: "whole-engine references unverified; no removal attempted", Query: "podman ps --all / container inspect"})
		}
		return s.finishCleanup(ctx, run, log, asset, out, err)
	}
	// Stdout and operation markers are attacker-controlled data, even inside apparent build windows.
	// Until a trusted producer supplies primary evidence, no stdout token grants ownership.
	trusted := []buildProof{}
	state := buildChannelState{}
	var results []BuildResult
	var proofErr error
	// The policy-test adapter supplies trusted results; production always reads the private channel and applies its vetoes.
	primaryChannel := s.BuildEvidence == nil
	if !primaryChannel {
		results, proofErr = s.BuildEvidence(ctx, run.options.RunDir)
	} else {
		state = readBuildChannel(run.options, run.before)
		results, proofErr = state.Results, state.Err
	}
	if proofErr != nil {
		run.report.Unverified = append(run.report.Unverified, CleanupItem{Reason: "some trusted build evidence incomplete or invalid; only independent primary results admitted", Query: "private build-result channel"})
	}
	blocked := map[string]string{}
	observedAttempt := map[string]string{}
	for id := range state.Inputs {
		blocked[id] = primaryInputReason
	}
	if primaryChannel {
		for _, r := range results {
			id := canonicalImageID(r.ID)
			if previous, ok := observedAttempt[id]; ok && previous != r.AttemptID && blocked[id] != primaryInputReason {
				blocked[id] = primarySharedReason
			}
			observedAttempt[id] = r.AttemptID
			if (r.Policy != SingleCommitPolicy || r.Reproducibility != "default-timestamps") && blocked[id] == "" {
				blocked[id] = primaryReuseReason
			}
		}
	}
	boundAsset := ""
	for _, result := range results {
		if len(result.Asset) != 64 || boundAsset != "" && boundAsset != result.Asset {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{Reason: "trusted asset identity inconsistent; no ownership admitted from contradictory invocation scope", Query: "private build-result channel"})
			results = nil
			trusted = nil
			state.Complete = false
			break
		}
		boundAsset = result.Asset
		if result.Source == "" || result.Step == "" {
			continue
		}
		proof := buildProof{Token: result.ID, Step: result.Step, Result: result.Source}
		if primaryChannel {
			proof.Reason = blocked[canonicalImageID(result.ID)]
		}
		trusted = append(trusted, proof)
	}
	if primaryChannel {
		run.report.BuildPolicy = "unverified"
		if state.Complete {
			run.report.BuildPolicy = SingleCommitPolicy
		} else if state.SingleCommitKnown {
			run.report.BuildPolicy = "single-commit-and-unverified"
		} else if state.LegacyKnown {
			run.report.BuildPolicy = "legacy-or-unverified"
		}
	}
	if len(results) > 0 {
		asset = results[0].Asset
	}
	record := uniqueBuildRecord(resolveBuildRecord(trusted, current))
	markEarlierRemovals(record, earlierRemovals(run))

	if err = cleanupWriteJSON(filepath.Join(run.directory, "build-record.json"), record, cleanupPrivateLimit); err != nil {
		return fmt.Errorf("cleanup: pending (cannot retain build record)")
	}
	for _, entry := range record {
		switch entry.Reason {
		case "":
		case cleanupEarlierReason:
			// Its bytes were counted by the earlier report; this one records only the fact.
			run.report.RemovedEarlier = append(run.report.RemovedEarlier, CleanupItem{ID: canonicalImageID(entry.Token), Reason: entry.Reason})
		default:
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: entry.Token, Reason: entry.Reason, Query: "build output / full inventory / image inspect"})
		}
	}
	plan := cleanupPlan(run.before, current, record, asset, options.Retain)
	if primaryChannel {
		accountBuildCoverage(run, plan, record, current, state)
		explainPrimaryVetoes(plan, record)
	}
	if err = cleanupWriteJSON(filepath.Join(run.directory, "removal-plan.json"), plan, cleanupSummaryLimit/2); err != nil {
		return fmt.Errorf("cleanup: pending (cannot retain removal plan)")
	}
	// Removals whose evidence was lost join Removed only once a successful final inventory omits them.
	evidenceLost := []CleanupItem{}
	plannedNames := map[string][]string{}
	for _, image := range current {
		plannedNames[image.ID] = image.RepoTags
	}
	for index, item := range plan {
		if item.Reason != "" {
			if item.Reason == "missing exact build proof" && run.report.Status == "complete" {
				run.report.Status = "partial"
			}
			run.report.Skipped = append(run.report.Skipped, item)
			continue
		}
		// Recheck the endpoint and every container immediately before each removal.
		if err = s.cleanupEngine(ctx, run, target); err != nil {
			if errors.Is(err, errCleanupUnreachable) && len(run.report.Removed) == 0 && len(evidenceLost) == 0 {
				return err
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
		// The engine lock does not exclude other clients, so reread the names a removal by ID would also drop.
		answer, inspectErr := s.cleanupQuery(ctx, run, target, "image", "inspect", item.ID)
		reread, verified := decodeCleanupImages(answer, []string{item.ID})
		if inspectErr != nil || !verified {
			item.Reason = "image names unverified before removal; retained"
			item.Query = "podman image inspect ID"
			run.report.Unverified = append(run.report.Unverified, item)
			continue
		}
		if !sameImageNames(plannedNames[item.ID], reread[item.ID].RepoTags) {
			item.Reason = "image names changed since planning"
			run.report.Skipped = append(run.report.Skipped, item)
			continue
		}
		if _, err = s.cleanupQuery(ctx, run, target, "rmi", "--no-prune", item.ID); errors.Is(err, errCleanupEvidenceLost) {
			// The removal ran; keep it for final reconciliation and keep the audit loss visible.
			run.report.Unverified = append(run.report.Unverified, CleanupItem{Reason: "removal evidence could not be retained; final inventory reconciles the removal", Query: "podman rmi --no-prune ID"})
			evidenceLost = append(evidenceLost, item)
			continue
		} else if err != nil {
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
		for _, item := range evidenceLost {
			if remaining[item.ID] {
				run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "removal evidence lost and image present in final inventory", Query: "podman images --all --no-trunc"})
			} else {
				run.report.Removed = append(run.report.Removed, item)
			}
		}
		for _, item := range run.report.RemovedEarlier {
			if remaining[item.ID] {
				run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "removed earlier but present in final inventory", Query: "podman images --all --no-trunc"})
			}
		}
		run.report.RemainingContainers = len(run.containers)
	} else {
		// A removal without retained evidence counts only when the final inventory confirms it.
		for _, item := range evidenceLost {
			run.report.Unverified = append(run.report.Unverified, CleanupItem{ID: item.ID, Reason: "removal evidence lost and final inventory unverified", Query: "podman images --all --no-trunc"})
		}
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
	if r.ended.LogUnverified {
		return nil, fmt.Errorf("cleanup: pending (private suite log unverified)")
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
	if r.options.RunDir != directory || !filepath.IsAbs(r.options.Checkout) || !filepath.IsAbs(r.options.RecordDir) || !filepath.IsAbs(r.options.LockRoot) || within(directory, r.options.RecordDir) || within(r.options.RecordDir, directory) || !revisionLine.MatchString(r.options.Revision) {
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
		data, err = s.query(ctx, r.options, "git", "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
		if err == nil && len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
	}
	_ = cleanupWriteJSON(filepath.Join(r.directory, "checkout-diagnostic.json"), map[string]string{"error": boundedCleanupText(fmt.Sprint(err), 4096)}, cleanupPrivateLimit)
	return fmt.Errorf("cleanup: pending (checkout changed or unverified)")
}

// sameImageNames reports whether two image name lists hold the same names, in any order.
func sameImageNames(planned, current []string) bool {
	if len(planned) != len(current) {
		return false
	}
	left, right := append([]string(nil), planned...), append([]string(nil), current...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
