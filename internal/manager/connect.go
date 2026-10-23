package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// connectTool runs one T3 Connect account action of an enabled Tool as agent: a login holds the
// Tool's connect lock for its whole interactive run, the other actions run under the manager lock,
// and a link restarts the service so that it takes effect.
func connectTool(root string, r toolRequest, stdout, stderr io.Writer, effects Effects) int {
	directory := toolDirectory(root, r.name)
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		return exitStatus(fmt.Errorf("%s not enabled; run tools enable NAME %s", r.name, r.name), stderr)
	}
	if r.action == "login" {
		release, held, err := tryLock(filepath.Join(directory, "connect.lock"))
		if err != nil {
			return exitStatus(err, stderr)
		}
		if !held {
			return exitStatus(errors.New("another tool change is in progress; retry"), stderr)
		}
		defer release()
	} else {
		unlock, err := lockAgentData(root)
		if err != nil {
			return exitStatus(err, stderr)
		}
		defer unlock()
	}
	// The enabled check runs under the lock, so a delete cannot slip in between.
	current, enabled, err := readToolSelection(directory)
	if err != nil {
		return exitStatus(err, stderr)
	}
	if !enabled {
		return exitStatus(fmt.Errorf("%s not enabled; run tools enable NAME %s", r.name, r.name), stderr)
	}
	switch r.action {
	case "login":
		return exitStatus(connectLogin(directory, current, r, stdout, stderr, effects), stderr)
	case "link":
		return exitStatus(connectLink(directory, current, r, stdout, stderr, effects), stderr)
	default:
		return exitStatus(runConnectAction(directory, current, r, stdout, stderr, effects), stderr)
	}
}

// connectLogin runs the interactive headless login and, when it fails, puts the account credential
// back as it was: removed when none existed, restored when one did.
func connectLogin(directory string, current toolSelection, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	credential := filepath.Join(toolOwnHome(directory), filepath.FromSlash(r.definition.CredentialPath))
	previous, readErr := os.ReadFile(credential)
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("cannot read the %s credential before the login: %w", r.name, readErr)
	}
	err := runConnectAction(directory, current, r, stdout, stderr, effects)
	if err == nil {
		return nil
	}
	var restore error
	if readErr == nil {
		restore = os.WriteFile(credential, previous, 0600)
	} else if remove := os.Remove(credential); remove != nil && !os.IsNotExist(remove) {
		restore = remove
	}
	if restore != nil {
		return fmt.Errorf("%w; the earlier credential state could not be restored: %v", err, restore)
	}
	return err
}

// connectLink runs the interactive headless link and restarts the service, because the link takes effect at the next server start.
func connectLink(directory string, current toolSelection, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	if err := runConnectAction(directory, current, r, stdout, stderr, effects); err != nil {
		return err
	}
	if err := stopSupervised(toolService(directory, r.name, current.Version, r.definition), effects); err != nil {
		return fmt.Errorf("%s link recorded, but the service did not stop for its restart: %w; use tools status NAME %s", r.name, err, r.name)
	}
	if err := startSupervised(toolService(directory, r.name, current.Version, r.definition), effects); err != nil {
		return fmt.Errorf("%s link recorded, but the service did not start again: %w; use tools status NAME %s", r.name, err, r.name)
	}
	fmt.Fprintf(stdout, "%s service restarted; the link takes effect\n", r.name)
	return nil
}

// runConnectAction runs the upstream connect command of the action as agent with the Tool's home
// and the caller's terminal. An interactive action watches for Ctrl+C, so an interrupted run is
// named as such whatever upstream's exit status; any other failure is named with that status, and
// upstream's own message, which precedes it, says why.
func runConnectAction(directory string, current toolSelection, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	args, err := r.definition.ResolveConnectAction(r.action)
	if err != nil {
		return err
	}
	var interrupts <-chan os.Signal
	if slices.Contains(r.definition.InteractiveConnectActions, r.action) {
		var stop func()
		interrupts, stop = watchInterrupts(effects)
		defer stop()
	}
	process := toolProcess(directory, current, r.definition, args)
	process.Stdin, process.Stdout, process.Stderr = effects.Stdin, stdout, stderr
	if err := effects.Start(process); err != nil {
		return interactiveFailure(r.name+" connect "+r.action, err, interrupts)
	}
	return nil
}

// upstreamConnectStatus is the part of the upstream status JSON that the Tool reports. Upstream
// reports reachability as `null` until a server answered and otherwise as an object whose `status`
// is `reachable` or names the failure (source-only, `apps/server/src/cli/connect.ts:213-223` at `044f072`).
type upstreamConnectStatus struct {
	Authenticated bool    `json:"authenticated"`
	Linked        bool    `json:"linked"`
	CloudUserID   *string `json:"cloudUserId"`
	RelayClient   struct {
		Status string `json:"status"`
	} `json:"relayClient"`
	Reachability json.RawMessage `json:"reachability"`
}

// readConnectStatus runs the upstream status command as agent on a healthy installation and
// condenses its JSON; a damaged installation or a failing command is reported in the status, not as an error.
func readConnectStatus(home, directory, name string, current toolSelection, definition catalog.Tool, effects Effects) *catalog.ConnectStatus {
	if len(definition.ConnectStatusArgs) == 0 {
		return nil
	}
	status := &catalog.ConnectStatus{}
	if !toolInstallationHealthy(home, directory, current.Version, definition) {
		// The launcher would download a runtime for a damaged installation; a status query must change nothing.
		status.Error = "status unavailable: the installation is damaged; run tools enable NAME " + name
		return status
	}
	var output, diagnostic bytes.Buffer
	process := toolProcess(directory, current, definition, definition.ConnectStatusArgs)
	process.Stdout, process.Stderr = &output, &diagnostic
	err := effects.Start(process)
	if err != nil {
		status.Error = "status unavailable: " + processFailureSummary(diagnostic.String(), err)
		return status
	}
	var upstream upstreamConnectStatus
	if err := json.Unmarshal(output.Bytes(), &upstream); err != nil {
		status.Error = "status unreadable: " + err.Error()
		return status
	}
	status.Authenticated, status.Linked, status.RelayClient = upstream.Authenticated, upstream.Linked, upstream.RelayClient.Status
	if upstream.CloudUserID != nil {
		status.Account = *upstream.CloudUserID
	}
	if reachability := strings.TrimSpace(string(upstream.Reachability)); reachability != "" && reachability != "null" {
		status.Reachability = reachability
		var report struct {
			Status string `json:"status"`
		}
		status.Reachable = json.Unmarshal(upstream.Reachability, &report) == nil && report.Status == "reachable"
	}
	return status
}
