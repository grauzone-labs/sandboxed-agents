package liverun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Cleanup artifact limits bound private inventories, coordinator evidence and suite-log input.
const (
	cleanupPrivateLimit = 16 * 1024 * 1024
	cleanupSummaryLimit = 4 * 1024 * 1024
	cleanupLogLimit     = 64 * 1024 * 1024
)

// CleanupItem reports only an immutable ID, sanitized tags, byte cost and fixed decision text.
type CleanupItem struct {
	ID           string   `json:"id,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SizeBytes    uint64   `json:"size_bytes"`
	SizeVerified bool     `json:"size_verified"`
	Reason       string   `json:"reason,omitempty"`
	Query        string   `json:"query,omitempty"`
}

// CleanupMeasurement reports filesystem roles and engine storage totals without host paths.
type CleanupMeasurement struct {
	FreeSpace []CleanupSpace `json:"free_space"`
	SystemDF  string         `json:"system_df"`
}

// CleanupSpace is one path-free filesystem measurement.
type CleanupSpace struct {
	Filesystem string `json:"filesystem"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// CleanupReport is bounded coordinator evidence; detailed engine output remains in the run directory.
type CleanupReport struct {
	BuildPolicy         string             `json:"build_policy,omitempty"`
	Status              string             `json:"status"`
	SuiteExitCode       int                `json:"suite_exit_code"`
	Interrupted         bool               `json:"interrupted"`
	Removed             []CleanupItem      `json:"removed"`
	Skipped             []CleanupItem      `json:"skipped"`
	Unverified          []CleanupItem      `json:"unverified"`
	Before              CleanupMeasurement `json:"before"`
	After               CleanupMeasurement `json:"after"`
	PostSuiteSeconds    float64            `json:"post_suite_seconds"`
	DurationsFile       string             `json:"durations_file,omitempty"`
	RemainingContainers int                `json:"remaining_containers"`
	Collected           bool               `json:"collected"`
}

// cleanupRead rejects nonregular and oversized artifacts before allocating their contents.
func cleanupRead(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > int64(limit) {
		return nil, fmt.Errorf("artifact exceeds limit or is not regular")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if len(data) > limit {
		return nil, fmt.Errorf("artifact exceeds limit")
	}
	return data, err
}

// cleanupWriteJSON refuses oversized output before atomically publishing a private artifact.
func cleanupWriteJSON(path string, value any, limit int) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > limit/2 {
		return fmt.Errorf("artifact exceeds size limit")
	}
	// writeJSON indents the compact value; enforce the actual formatted bound as well.
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(formatted)+1 > limit {
		return fmt.Errorf("artifact exceeds size limit")
	}
	return writeJSON(path, value)
}

// cleanupMeasurement retains only documented filesystem roles and allowlisted system-df rows.
func cleanupMeasurement(disks []Disk, df string) CleanupMeasurement {
	result := CleanupMeasurement{FreeSpace: []CleanupSpace{}, SystemDF: "unverified"}
	for _, disk := range disks {
		if disk.Name == "engine" || disk.Name == "host" {
			result.FreeSpace = append(result.FreeSpace, CleanupSpace{Filesystem: disk.Name, FreeBytes: disk.FreeBytes})
		}
	}
	rows := []string{}
	row := regexp.MustCompile(`^(Images|Containers|Local Volumes|Build Cache)\s+[0-9]+\s+[0-9]+\s+[0-9.]+\s*(?:B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB)\s+[0-9.]+\s*(?:B|kB|KB|MB|GB|TB|KiB|MiB|GiB|TiB)(?:\s+\([0-9.]+%\))?$`)
	for _, line := range strings.Split(df, "\n") {
		line = strings.TrimSpace(line)
		if row.MatchString(line) {
			rows = append(rows, line)
		}
		if len(rows) == 4 {
			break
		}
	}
	if len(rows) > 0 {
		result.SystemDF = "TYPE TOTAL ACTIVE SIZE RECLAIMABLE\n" + strings.Join(rows, "\n")
	}
	return result
}

// finishCleanup measures the final state, retains the report and recollects durations with both intervals.
func (s *Service) finishCleanup(ctx context.Context, r *cleanupRun, log []byte, asset string, out io.Writer, prior error) error {
	target := r.before.Target
	disks, err := s.disks(ctx, r.options, target)
	if err != nil {
		r.report.Unverified = append(r.report.Unverified, CleanupItem{Reason: "after filesystem measurement failed", Query: "filesystem free space"})
		disks = nil
	}
	df, dfErr := s.cleanupQuery(ctx, r, target, "system", "df")
	if dfErr != nil {
		r.report.Unverified = append(r.report.Unverified, CleanupItem{Reason: "after engine measurement failed", Query: "podman system df"})
	}
	if err = cleanupWriteJSON(filepath.Join(r.directory, "after-measurement.json"), struct {
		Disks    []Disk
		SystemDF string
	}{disks, string(df)}, cleanupPrivateLimit); err != nil {
		return fmt.Errorf("cleanup: pending (cannot retain after measurement)")
	}
	r.report.After = cleanupMeasurement(disks, string(df))
	if prior != nil || len(r.report.Unverified) > 0 {
		r.report.Status = "unverified"
	}
	// Persist the completed cleanup/report work before taking its end timestamp.
	if err = cleanupWriteJSON(filepath.Join(r.directory, "cleanup-report.json"), r.report, cleanupSummaryLimit); err != nil {
		return fmt.Errorf("cleanup report exceeds limit or cannot be retained")
	}
	end := time.Now().UTC()
	r.report.PostSuiteSeconds = end.Sub(r.ended.EndedAt).Seconds()
	load, base, cache := classifyContext(r.before, asset)
	platform := s.platformName()
	record := filepath.Join(r.options.RecordDir, "live-suite-"+platform+".json")
	args := []string{"run", "./internal/validationduration/cmd/retain-durations", "-record", record, "-host-load", load, "-base-image", base, "-build-cache", cache, "-preparation-start", r.started.PreparationStart.Format(time.RFC3339Nano), "-preparation-end", r.started.PreparationEnd.Format(time.RFC3339Nano), "-post-suite-start", r.ended.EndedAt.Format(time.RFC3339Nano), "-post-suite-end", end.Format(time.RFC3339Nano)}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	data, collectErr := s.Executor.Execute(bounded, Invocation{Program: "go", Args: args, Dir: r.options.Checkout, Env: hostEnvironment(s.Platform, nil), Input: log})
	expected := filepath.Join(r.options.RecordDir, "durations-"+platform+".json")
	collected, optional := collectorOutcome(data, collectErr, expected)
	if collected {
		if _, readErr := cleanupRead(expected, cleanupSummaryLimit); readErr == nil {
			r.report.Collected = true
			r.report.DurationsFile = filepath.Base(expected)
			if optional {
				r.report.Unverified = append(r.report.Unverified, CleanupItem{Reason: "case durations retained; optional operation evidence unverified", Query: "retain-durations"})
				r.report.Status = "unverified"
			}
			collectErr = nil
		} else {
			collectErr = fmt.Errorf("collector output artifact unreadable or oversized")
		}
	} else if collectErr == nil {
		collectErr = fmt.Errorf("collector did not confirm this invocation output")
	}
	if collectErr != nil {
		_ = cleanupWriteJSON(filepath.Join(r.directory, "collector-diagnostic.json"), map[string]string{"error": boundedCleanupText(collectErr.Error(), 4096), "output": boundedCleanupText(string(data), 4096)}, cleanupPrivateLimit)
		r.report.Unverified = append(r.report.Unverified, CleanupItem{Reason: "final durations collection failed; earlier file is not current evidence", Query: "retain-durations"})
		r.report.Status = "unverified"
	}
	if err = cleanupWriteJSON(filepath.Join(r.options.RecordDir, "cleanup-"+platform+".json"), r.report, cleanupSummaryLimit); err != nil {
		return fmt.Errorf("sanitized cleanup report cannot be retained")
	}
	encoded, err := json.Marshal(r.report)
	if err != nil || len(encoded)+1 > cleanupSummaryLimit {
		return fmt.Errorf("sanitized cleanup report exceeds limit")
	}
	n, err := out.Write(append(encoded, '\n'))
	if err != nil {
		return err
	}
	if n != len(encoded)+1 {
		return io.ErrShortWrite
	}
	r.published = true
	if r.report.Status == "partial" {
		return fmt.Errorf("cleanup: partial (unproven images retained; see sanitized cleanup report)")
	}
	if r.report.Status != "complete" {
		return fmt.Errorf("cleanup: unverified (see sanitized cleanup report)")
	}
	return nil
}
