package manager

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// parsePairRequest validates `tools pair TOOL [--ttl DURATION]` and `tools pair TOOL --revoke`.
func parsePairRequest(r toolRequest, args []string, stderr io.Writer) (toolRequest, bool) {
	f := flag.NewFlagSet("tools pair", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&r.ttl, "ttl", "", "how long the pairing code stays valid")
	f.BoolVar(&r.revoke, "revoke", false, "revoke every unused pairing code instead")
	if err := f.Parse(args); err != nil {
		return r, false
	}
	if f.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected tools arguments")
		return r, false
	}
	if r.ttl != "" && r.revoke {
		fmt.Fprintln(stderr, "--ttl and --revoke conflict")
		return r, false
	}
	if r.ttl != "" {
		if err := catalog.ValidatePairingTTL(r.ttl); err != nil {
			fmt.Fprintln(stderr, err)
			return r, false
		}
	}
	return r, true
}

// pairTool issues a one-time pairing code for the Tool's tailnet HTTPS origin, or revokes the unused
// codes, as agent and without the manager lock (ADR-0010).
func pairTool(home, directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	current, enabled, err := readToolSelection(directory)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("%s not enabled; run tools enable NAME %s", r.name, r.name)
	}
	if !toolInstallationHealthy(home, directory, current.Version, r.definition) {
		return fmt.Errorf("%s installation is damaged; run tools enable NAME %s", r.name, r.name)
	}
	if r.revoke {
		return revokePairingCodes(directory, current, r, stdout, effects)
	}
	if err := checkPairingPrerequisites(directory, r, effects); err != nil {
		return err
	}
	// The codes that exist before the pairing stay when it fails afterwards; only the one it issued is revoked then.
	before, err := listPairingCodes(directory, current, r.definition, effects)
	if err != nil {
		return fmt.Errorf("%s pair: %w", r.name, err)
	}
	if err := runPair(directory, current, r, stdout, stderr, effects); err != nil {
		// Upstream mints the code last, so a failed or interrupted run leaves none; should one exist, it goes.
		if revoked, revoke := revokeIssued(directory, current, r.definition, before, effects); revoke != nil {
			return fmt.Errorf("%w; a code it may have issued could not be revoked: %v; run tools pair NAME %s --revoke", err, revoke, r.name)
		} else if revoked > 0 {
			return fmt.Errorf("%w; the code it issued was revoked", err)
		}
		return err
	}
	// Upstream probes the origin from inside the sandbox, which has no route to the tailnet in userspace
	// networking mode, so its note that the endpoint has not answered yet is expected; the node's own Serve
	// configuration is the check that the mapping took.
	target, err := tailnetServeTarget(directory, effects)
	if err != nil || target != r.definition.ServiceOrigin {
		if _, revoke := revokeIssued(directory, current, r.definition, before, effects); revoke != nil {
			return fmt.Errorf("%s pair: the tailnet node does not front the service after the pairing, and the issued code could not be revoked: %v; run tools pair NAME %s --revoke", r.name, revoke, r.name)
		}
		return fmt.Errorf("%s pair: the tailnet node does not front the service after the pairing; the issued code was revoked; see tools status NAME %s", r.name, r.name)
	}
	fmt.Fprintf(stdout, "\nThe code above is single-use and valid until the printed expiry; this command does not confirm the scan. Paired devices appear in tools status NAME %s.\n", r.name)
	return nil
}

// unknownSandboxName stands for the sandbox in the manager's messages, because the manager runs inside the sandbox and never learns its name.
const unknownSandboxName = "NAME"

// checkPairingPrerequisites reads the service, the tailnet node, and the node's sign-in and Serve state
// as one status and refuses, before anything changes, with the shared step-8 messages; the manager knows
// no sandbox name, so the messages say NAME.
func checkPairingPrerequisites(directory string, r toolRequest, effects Effects) error {
	status := ToolStatus{Name: r.name}
	if err := fillServiceStatus(&status, directory, effects); err != nil {
		return err
	}
	const querySignIn = true
	if err := fillTailnetStatus(&status, directory, r.name, r.definition, querySignIn, effects); err != nil {
		return err
	}
	return catalog.PairingPrerequisites(unknownSandboxName, status)
}

// runPair runs upstream's `t3 pair --tailscale` on the caller's terminal, with the Tool's tailscale
// command first on its PATH, and watches for Ctrl+C so an interrupted pairing is named as such.
func runPair(directory string, current toolSelection, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	interrupts, stop := watchInterrupts(effects)
	defer stop()
	args := append([]string(nil), r.definition.PairArgs...)
	if r.ttl != "" {
		args = append(args, r.definition.TTLOption, r.ttl)
	}
	process := toolProcess(directory, current, r.definition, args)
	process.Environment = append(process.Environment, "PATH="+filepath.Dir(tailnetWrapper(directory))+":"+os.Getenv("PATH"))
	process.Stdin, process.Stdout, process.Stderr = effects.Stdin, stdout, stderr
	if err := effects.Start(process); err != nil {
		return interactiveFailure(r.name+" pair", err, interrupts)
	}
	return nil
}

// upstreamPairingCode is the part of upstream's pairing list the Tool reads.
type upstreamPairingCode struct {
	ID string `json:"id"`
}

// listPairingCodes reads the ids of the unused pairing codes from upstream's database.
func listPairingCodes(directory string, current toolSelection, definition catalog.Tool, effects Effects) ([]string, error) {
	output, err := launcherOutput(directory, current, definition, effects, definition.PairingListArgs...)
	if err != nil {
		return nil, fmt.Errorf("cannot list the pairing codes: %w", err)
	}
	var codes []upstreamPairingCode
	if err := json.Unmarshal([]byte(output), &codes); err != nil {
		return nil, fmt.Errorf("cannot read the pairing codes: %w", err)
	}
	ids := make([]string, 0, len(codes))
	for _, code := range codes {
		ids = append(ids, code.ID)
	}
	return ids, nil
}

// revokeCodes revokes the named pairing codes one by one, as upstream offers no revoke-all.
func revokeCodes(directory string, current toolSelection, definition catalog.Tool, ids []string, effects Effects) error {
	for _, id := range ids {
		if _, err := launcherOutput(directory, current, definition, effects, append(append([]string(nil), definition.PairingRevokeArgs...), id)...); err != nil {
			return fmt.Errorf("cannot revoke the pairing code %s: %w", id, err)
		}
	}
	return nil
}

// revokeAll revokes every unused pairing code and reports how many.
func revokeAll(directory string, current toolSelection, definition catalog.Tool, effects Effects) (int, error) {
	ids, err := listPairingCodes(directory, current, definition, effects)
	if err != nil {
		return 0, err
	}
	return len(ids), revokeCodes(directory, current, definition, ids, effects)
}

// revokeIssued revokes the unused codes that did not exist before a pairing, the ones that pairing issued, and reports how many.
func revokeIssued(directory string, current toolSelection, definition catalog.Tool, before []string, effects Effects) (int, error) {
	after, err := listPairingCodes(directory, current, definition, effects)
	if err != nil {
		return 0, err
	}
	var issued []string
	for _, id := range after {
		if !slices.Contains(before, id) {
			issued = append(issued, id)
		}
	}
	return len(issued), revokeCodes(directory, current, definition, issued, effects)
}

// revokePairingCodes runs `tools pair TOOL --revoke`: it acts on upstream's database, with or without a running service.
func revokePairingCodes(directory string, current toolSelection, r toolRequest, stdout io.Writer, effects Effects) error {
	revoked, err := revokeAll(directory, current, r.definition, effects)
	if err != nil {
		return fmt.Errorf("%s pair --revoke: %w", r.name, err)
	}
	switch revoked {
	case 0:
		fmt.Fprintf(stdout, "%s: no unused pairing code\n", r.name)
	case 1:
		fmt.Fprintf(stdout, "%s: revoked 1 unused pairing code\n", r.name)
	default:
		fmt.Fprintf(stdout, "%s: revoked %d unused pairing codes\n", r.name, revoked)
	}
	return nil
}

// upstreamSession is the part of upstream's session list the Tool reads.
type upstreamSession struct {
	Client struct {
		Label      string `json:"label"`
		DeviceType string `json:"deviceType"`
		OS         string `json:"os"`
	} `json:"client"`
	Connected       bool    `json:"connected"`
	LastConnectedAt *string `json:"lastConnectedAt"`
}

// readPairedDevices lists the Tool's device sessions from upstream's database on a healthy installation; a failure is reported in the result, not as an error.
func readPairedDevices(home, directory, name string, current toolSelection, definition catalog.Tool, effects Effects) *catalog.DeviceReport {
	if len(definition.SessionListArgs) == 0 {
		return nil
	}
	report := &catalog.DeviceReport{}
	if !toolInstallationHealthy(home, directory, current.Version, definition) {
		report.Error = "devices unavailable: the installation is damaged; run tools enable NAME " + name
		return report
	}
	output, err := launcherOutput(directory, current, definition, effects, definition.SessionListArgs...)
	if err != nil {
		report.Error = "devices unavailable: " + err.Error()
		return report
	}
	var sessions []upstreamSession
	if err := json.Unmarshal([]byte(output), &sessions); err != nil {
		report.Error = "devices unreadable: " + err.Error()
		return report
	}
	for _, session := range sessions {
		device := catalog.PairedDevice{Client: session.Client.Label, Connected: session.Connected}
		if device.Client == "" {
			device.Client = strings.TrimSpace(session.Client.DeviceType)
			if session.Client.OS != "" {
				device.Client = strings.TrimSpace(device.Client + " (" + session.Client.OS + ")")
			}
		}
		if session.LastConnectedAt != nil {
			device.LastConnected = *session.LastConnectedAt
		}
		report.Items = append(report.Items, device)
	}
	return report
}

// launcherOutput runs one launcher command of the Tool as agent and returns its standard output, or an error that carries the first line of its diagnostic.
func launcherOutput(directory string, current toolSelection, definition catalog.Tool, effects Effects, args ...string) (string, error) {
	return processOutput(toolProcess(directory, current, definition, args), effects)
}
