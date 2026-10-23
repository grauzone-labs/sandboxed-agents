package manualcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// refusalPaths lists every command path of the grammar that takes a sandbox name, except
// check, list, and update, which recover or report.
func refusalPaths(sandbox string) [][]string {
	return [][]string{
		{"up", sandbox},
		{"start", sandbox},
		{"start", sandbox, "--ssh-config"},
		{"stop", sandbox},
		{"restart", sandbox},
		{"remove", sandbox},
		{"shell", sandbox},
		{"ssh-config", sandbox},
		{"ssh-config", sandbox, "--install"},
		{"ssh-config", sandbox, "--remove"},
		{"fingerprint", sandbox},
		{"agents", "enable", sandbox, "opencode"},
		{"agents", "disable", sandbox, "opencode"},
		{"agents", "update", sandbox, "opencode"},
		{"agents", "login", sandbox, "opencode", "provider"},
		{"agents", "status", sandbox, "opencode"},
		{"agents", "run", sandbox, "opencode", "--version"},
		{"agents", "session", sandbox, "opencode"},
		{"agents", "session", sandbox, "opencode", "--stop"},
		{"integrations", "login", sandbox, "github"},
		{"integrations", "login", sandbox, "azure", "device-code"},
		{"integrations", "login", sandbox, "azure", "browser"},
		{"integrations", "login", sandbox, "azdo", "--organization", "https://dev.azure.com/example-organization"},
		{"integrations", "config", sandbox, "git", "identity", "--name", "Manual-Check", "--email", "manual-check@example.invalid"},
		{"integrations", "config", sandbox, "git", "credentials"},
	}
}

// UpdateInterrupted runs item `update-interrupted`: it creates the sandbox, starts
// `update NAME --with native` in the background, kills exactly that process through its handle
// once the backup container exists, observes check, list, the 25 refusals, the unchanged state,
// and the recovery, and removes the sandbox unless keep is set.
func (r *Run) UpdateInterrupted(sandbox string, keep bool) error {
	if !namePattern.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	dir, err := r.ItemDir("update-interrupted")
	if err != nil {
		return err
	}
	failures := &Failures{run: r}
	paths := refusalPaths(sandbox)
	var listing strings.Builder
	for _, path := range paths {
		listing.WriteString(strings.Join(path, " ") + "\n")
	}
	if err := writePrivate(filepath.Join(dir, "refusals.txt"), listing.String()); err != nil {
		return err
	}

	// 1. A fresh sandbox.
	up, err := Step(dir, "up", nil, r.SA("up", sandbox))
	if err != nil {
		return err
	}
	if up.Exit != 0 {
		return fmt.Errorf("up %s failed with exit %d; see %s", sandbox, up.Exit, up.Log)
	}

	// 2. The update in the background; the process handle Go holds is the only thing killed.
	log, err := os.OpenFile(filepath.Join(dir, "update-interrupted.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	update := r.SA("update", sandbox, "--with", "native")
	update.Stdout, update.Stderr = log, log
	if err := update.Start(); err != nil {
		log.Close()
		return err
	}
	meta := map[string]any{"update_pid": update.Process.Pid, "identity": processIdentity(update.Process.Pid), "started_utc": utcNow(), "backup_seen": false}
	deadline := time.Now().Add(2 * time.Minute)
	done := make(chan error, 1)
	go func() { done <- update.Wait() }()
	var waitErr error
	finished := false
	for !finished && time.Now().Before(deadline) {
		if r.ContainerExists(r.BackupContainer(sandbox)) {
			meta["backup_seen"], meta["backup_seen_utc"] = true, utcNow()
			if err := update.Process.Kill(); err != nil {
				meta["kill_error"] = err.Error()
			} else {
				meta["kill_pid"], meta["kill_signal"] = update.Process.Pid, "KILL"
			}
			break
		}
		select {
		case waitErr = <-done:
			finished = true
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !finished {
		waitErr = <-done
	}
	log.Close()
	meta["update_wait_result"] = fmt.Sprint(waitErr)
	meta["finished_utc"] = utcNow()
	if err := writePrivateJSON(filepath.Join(dir, "interruption.json"), meta); err != nil {
		return err
	}
	if meta["backup_seen"] != true {
		if err := r.Verdict(dir, "update-interrupted", true, "backup not seen before the update ended; not observed; repeat with a fresh sandbox"); err != nil {
			return err
		}
		return fmt.Errorf("update-interrupted: not observed")
	}
	r.Printf("killed update PID %d after the backup container appeared\n", update.Process.Pid)

	// 3. The observations that must change nothing.
	if err := r.CaptureState(filepath.Join(dir, "before-int.txt"), sandbox); err != nil {
		return err
	}
	check, err := Step(dir, "check", nil, r.SA("check", sandbox))
	if err != nil {
		return err
	}
	if check.Exit == 0 || !check.Contains("interrupted update") {
		failures.Addf("check did not report the interrupted update non-zero (exit %d)", check.Exit)
	}
	list, err := Step(dir, "list", nil, r.SA("list"))
	if err != nil {
		return err
	}
	if !strings.Contains(ListState(list.Output(), sandbox), "update interrupted") {
		failures.Addf("list does not show %s as update interrupted", sandbox)
	}
	var results strings.Builder
	for index, path := range paths {
		null, err := nullStdin()
		if err != nil {
			return err
		}
		result, err := Step(dir, fmt.Sprintf("refusal-%02d", index+1), null, r.SA(path...))
		null.Close()
		if err != nil {
			return err
		}
		names := result.Contains("update " + sandbox)
		line := fmt.Sprintf("%s | exit %d | names update %s: %t", strings.Join(path, " "), result.Exit, sandbox, names)
		results.WriteString(line + "\n")
		r.Printf("%s\n", line)
		if result.Exit == 0 || !names {
			failures.Addf("command path %q was not refused naming update %s", strings.Join(path, " "), sandbox)
		}
	}
	if err := writePrivate(filepath.Join(dir, "refusals-results.txt"), results.String()); err != nil {
		return err
	}
	if err := r.CaptureState(filepath.Join(dir, "after-int.txt"), sandbox); err != nil {
		return err
	}
	if err := CompareFiles(filepath.Join(dir, "before-int.txt"), filepath.Join(dir, "after-int.txt")); err != nil {
		failures.Addf("%v", err)
	}

	// 4. The recovery, which exits as the state determines, builds no image and leaves the
	// volumes as they are, and the next update.
	if err := r.ImageInventory(filepath.Join(dir, "images-before-recovery.txt")); err != nil {
		return err
	}
	volumesBefore, err := r.PodmanOutput("volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	recovery, err := Step(dir, "recovery", nil, r.SA("update", sandbox))
	if err != nil {
		return err
	}
	if recovery.Exit != 0 && !recovery.Contains("restored") {
		failures.Addf("recovery exited %d without restoring", recovery.Exit)
	}
	if r.ContainerExists(r.BackupContainer(sandbox)) {
		failures.Addf("a backup container remains after the recovery")
	}
	if err := r.ImageInventory(filepath.Join(dir, "images-after-recovery.txt")); err != nil {
		return err
	}
	// CompareImages lists the IDs of its first inventory that the second lacks, so the two calls
	// name the images the recovery added and the images it removed; both must be empty.
	before, after := filepath.Join(dir, "images-before-recovery.txt"), filepath.Join(dir, "images-after-recovery.txt")
	if added, err := CompareImages(after, before); err != nil {
		failures.Addf("%v", err)
	} else if len(added) > 0 {
		failures.Addf("the recovery added images (built or pulled): %v", added)
	}
	if removed, err := CompareImages(before, after); err != nil {
		failures.Addf("%v", err)
	} else if len(removed) > 0 {
		failures.Addf("the recovery removed images: %v", removed)
	}
	volumesAfter, err := r.PodmanOutput("volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if sortedLines(volumesBefore) != sortedLines(volumesAfter) {
		failures.Addf("the volumes changed across the recovery")
	}
	if err := writePrivate(filepath.Join(dir, "volumes-recovery.txt"), "before:\n"+sortedLines(volumesBefore)+"after:\n"+sortedLines(volumesAfter)); err != nil {
		return err
	}
	follow, err := Step(dir, "follow-up", nil, r.SA("update", sandbox, "--with", "native"))
	if err != nil {
		return err
	}
	if follow.Exit != 0 {
		failures.Addf("the follow-up update exited %d", follow.Exit)
	}
	if !keep {
		remove, err := Step(dir, "remove", nil, r.SA("remove", sandbox, "--volumes"))
		if err != nil {
			return err
		}
		if remove.Exit != 0 {
			failures.Addf("remove exited %d", remove.Exit)
		}
	}
	return failures.Finish(dir, "update-interrupted", fmt.Sprintf("killed PID %d after the backup appeared; check and list reported the interruption; %d refusals non-zero naming update %s; state unchanged; recovery exit %d; follow-up update exit %d", update.Process.Pid, len(paths), sandbox, recovery.Exit, follow.Exit))
}
