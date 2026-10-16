package cli

import (
	"fmt"
	"io"
)

// recoverUpdate uses Podman alone to finish or restore an interrupted update.
func recoverUpdate(request Request, p podmanTarget, objects sandboxObjects, output io.Writer) error {
	retainBackup := func(reason string) error {
		return fmt.Errorf("sandbox %q: %s; backup retained; use Podman to inspect the objects", request.Name, reason)
	}
	if objects.backupContainer.State.Running == nil {
		return retainBackup("backup running state is missing")
	}
	names := DerivedNames(request.Group, request.Name)
	backupRunning := *objects.backupContainer.State.Running
	previous := objects.container.Config.Labels[updateWasRunningLabel]
	known := previous == "true" || previous == "false"
	running := previous == "true"
	if backupRunning {
		// The old sandbox has not been stopped yet, so its state is authoritative.
		running, known = true, true
	}
	restore := func(cause error) error {
		if objects.exists {
			if _, err := p.query("rm", "--force", names.Container); err != nil {
				return fmt.Errorf("%w; could not remove replacement: %v; backup retained", cause, err)
			}
		}
		if _, err := p.query("rename", names.Backup, names.Container); err != nil {
			return fmt.Errorf("%w; could not restore sandbox name: %v; backup retained", cause, err)
		}
		if running && !backupRunning {
			if _, err := p.query("start", names.Container); err != nil {
				return fmt.Errorf("%w; restored sandbox %s but could not start it: %v; use start %s", cause, request.Name, err, request.Name)
			}
		}
		if !known {
			return fmt.Errorf("%w; restored sandbox %s; previous state could not be determined, sandbox stays stopped; use start %s", cause, request.Name, request.Name)
		}
		return fmt.Errorf("%w; restored sandbox %s to its previous state; run update %s again to update", cause, request.Name, request.Name)
	}
	if backupRunning || !objects.exists || !known {
		return restore(fmt.Errorf("sandbox %s: interrupted update was not completed", request.Name))
	}
	if objects.container.State.Running == nil {
		return restore(fmt.Errorf("sandbox %s: replacement running state is missing", request.Name))
	}
	port, err := parseSSHPort(objects.container.Config.Labels[SSHPortLabel])
	if err != nil {
		return restore(fmt.Errorf("sandbox %s: recorded SSH port is missing or invalid", request.Name))
	}
	if _, err := recordedWorkspaceMount(names, objects); err != nil {
		return restore(fmt.Errorf("sandbox %s: replacement storage is missing or unsupported: %w", request.Name, err))
	}
	if !*objects.container.State.Running {
		if _, err := p.query("start", names.Container); err != nil {
			return restore(fmt.Errorf("could not start replacement: %w", err))
		}
	}
	if err := readySandbox(p, names.Container, port); err != nil {
		return restore(err)
	}
	if err := finishUpdate(request, p, running); err != nil {
		return err
	}
	fmt.Fprintf(output, "sandbox %s interrupted update cleaned up\n", request.Name)
	return nil
}
