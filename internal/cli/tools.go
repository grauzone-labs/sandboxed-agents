package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// toolManagerArgs builds the Podman arguments that start the manager directly as agent for Tool work (ADR-0009).
func toolManagerArgs(request Request, payload ...string) []string {
	return append([]string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", DerivedNames(request.Group, request.Name).Container, managerPath, "tools"}, payload...)
}

// administerTool enforces the ordered sandbox checks and dispatches a validated Tool operation to the manager as agent.
func administerTool(request Request, p podmanTarget, stdout io.Writer) error {
	if _, err := requireAgentManager(request, p); err != nil {
		return err
	}
	verb := strings.TrimPrefix(request.Command, "tools ")
	if verb == "status" {
		return printToolStatus(request, p, stdout)
	}
	if verb == "update" {
		status, err := readToolStatus(request, p, "")
		if err != nil {
			return err
		}
		if !status.Enabled {
			return fmt.Errorf("%s not enabled; run tools enable %s %s", request.Args[1], request.Name, request.Args[1])
		}
	}
	payload := []string{verb, request.Args[1]}
	for _, option := range []string{"channel", "version"} {
		if value := request.Options[option]; value != "" {
			payload = append(payload, "--"+option, value)
		}
	}
	var output, diagnostic bytes.Buffer
	exit, err := p.run(nil, &output, &diagnostic, toolManagerArgs(request, payload...)...)
	if err != nil || podmanTransportFailure(exit) {
		if err == nil {
			err = fmt.Errorf("manager request failed with exit status %d: %s", exit, strings.TrimSpace(diagnostic.String()))
		}
		return agentManagerError(request, err)
	}
	_, outputErr := io.Copy(stdout, &output)
	if exit != 0 {
		return errors.Join(fmt.Errorf("tools %s %s: %s", verb, request.Args[1], strings.TrimSpace(diagnostic.String())), outputErr)
	}
	return outputErr
}

// readToolStatus asks the manager, as agent, for the Tool selection, service, and node state, with the
// upstream queries the flag selects (`--connect` for everything, `--tailnet` for the node's sign-in and
// Serve state, none for the records alone), and validates the answer before callers use it.
func readToolStatus(request Request, p podmanTarget, query string) (catalog.ToolStatus, error) {
	var status catalog.ToolStatus
	payload := []string{"status", request.Args[1]}
	if query != "" {
		payload = append(payload, query)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	answer, err := p.queryContext(ctx, time.Second, toolManagerArgs(request, payload...)...)
	if err != nil {
		if ctx.Err() == nil && managerOperationFailure(err) {
			return status, err
		}
		return status, agentManagerError(request, errors.Join(err, ctx.Err()))
	}
	if err := json.Unmarshal([]byte(answer), &status); err != nil || status.Name != request.Args[1] || (status.Enabled && (status.InstalledVersion == "" || (status.Channel == "") == (status.Pin == ""))) {
		return status, agentManagerError(request, fmt.Errorf("invalid tool status answer"))
	}
	return status, nil
}

// printToolStatus renders the installed version, the channel or pin, the service, the T3 Connect state, the tailnet node, and the paired devices, or an absent selection.
func printToolStatus(request Request, p podmanTarget, stdout io.Writer) error {
	status, err := readToolStatus(request, p, "--connect")
	if err != nil {
		return err
	}
	if !status.Enabled {
		_, err = fmt.Fprintf(stdout, "%s: not enabled\n", status.Name)
		return err
	}
	channel, pin := status.Channel, status.Pin
	if channel == "" {
		channel = "none"
	}
	if pin == "" {
		pin = "none"
	}
	_, err = fmt.Fprintf(stdout, "%s: enabled; version %s; channel %s; pin %s; %s%s%s%s\n", status.Name, status.InstalledVersion, channel, pin, describeToolService(status), describeToolConnect(status.Connect), describeToolTailnet(request, status.Tailnet), describeToolDevices(status.Devices))
	return err
}

// runToolTerminalWork runs `tools tailscale` and `tools pair` through the manager as agent: after the
// sandbox checks they need an enabled Tool, a pairing needs a running service and a signed-in tailnet node
// with its prerequisites (step 8 of the order of checks), and the interactive forms need a terminal (step 9)
// before anything changes; `--revoke` and `logout` run without one.
func runToolTerminalWork(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	names, err := requireAgentManager(request, p)
	if err != nil {
		return err
	}
	query := ""
	if request.Command == "tools pair" && request.Options["revoke"] == "" {
		query = "--tailnet"
	}
	status, err := readToolStatus(request, p, query)
	if err != nil {
		return err
	}
	tool := request.Args[1]
	if !status.Enabled {
		return fmt.Errorf("%s not enabled; run tools enable %s %s", tool, request.Name, tool)
	}
	var payload []string
	var label string
	needsTerminal := true
	switch request.Command {
	case "tools pair":
		label = "tools pair " + tool
		payload = []string{"tools", "pair", tool}
		if request.Options["revoke"] != "" {
			label += " --revoke"
			payload = append(payload, "--revoke")
			needsTerminal = false
		} else {
			if err := catalog.PairingPrerequisites(request.Name, status); err != nil {
				return err
			}
			if ttl := request.Options["ttl"]; ttl != "" {
				payload = append(payload, "--ttl", ttl)
			}
		}
	default:
		action := request.Args[2]
		label = "tools tailscale " + tool + " " + action
		payload = []string{"tools", "tailscale", tool, action}
		if action == "login" {
			payload = append(payload, "--hostname", tailnetHostname(request.Name))
		} else {
			needsTerminal = false
		}
	}
	interactive := p.host.IsInputTerminal(p.host.Stdin) && p.host.IsOutputTerminal(stdout)
	if needsTerminal && !interactive {
		return fmt.Errorf("%s needs an interactive terminal", label)
	}
	exit, err := p.run(p.host.Stdin, stdout, stderr, agentExecArgs(interactive, names.Container, payload...)...)
	if err != nil {
		return err
	}
	if podmanTransportFailure(exit) {
		return agentManagerError(request, fmt.Errorf("manager request failed with exit status %d", exit))
	}
	if exit != 0 {
		return fmt.Errorf("%s failed", label)
	}
	return nil
}

// tailnetHostname derives the tailnet node's hostname from the sandbox name: lowercased, every
// character other than a letter, digit, or hyphen replaced by a hyphen, without leading or trailing
// hyphens, and at most 63 characters, so that the MagicDNS name reads as the sandbox's name.
func tailnetHostname(name string) string {
	var label strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			label.WriteRune(r)
		} else {
			label.WriteRune('-')
		}
	}
	hostname := strings.Trim(label.String(), "-")
	if len(hostname) > 63 {
		hostname = strings.TrimRight(hostname[:63], "-")
	}
	if hostname == "" {
		hostname = "sandbox"
	}
	return hostname
}

// describeToolTailnet renders the tailnet node of a Tool: absent, its supervised state, and once signed in its name, origin, Serve mapping, and capabilities.
func describeToolTailnet(request Request, tailnet *catalog.TailnetStatus) string {
	tool := request.Args[1]
	if tailnet == nil {
		return fmt.Sprintf("; tailnet node absent; run tools tailscale %s %s login", request.Name, tool)
	}
	text := "; tailnet node " + tailnet.Node
	if tailnet.Node != catalog.ServiceRunning {
		if tailnet.NodeFailure != "" {
			text += ": " + tailnet.NodeFailure
		}
		if tailnet.NodeLog != "" {
			text += "; node log " + tailnet.NodeLog
		}
		return text
	}
	switch {
	case tailnet.Error != "":
		return text + "; tailnet " + tailnet.Error
	case tailnet.Backend != catalog.TailnetBackendRunning:
		return fmt.Sprintf("%s, not signed in (%s); run tools tailscale %s %s login", text, tailnet.Backend, request.Name, tool)
	case tailnet.Name == "":
		return text + ", signed in; the tailnet has no MagicDNS name; enable MagicDNS at https://login.tailscale.com/admin/dns"
	}
	text += ", signed in as " + tailnet.Name + ", origin " + tailnet.Origin
	switch {
	case tailnet.Serve == "":
		text += fmt.Sprintf("; no Serve mapping yet; run tools pair %s %s", request.Name, tool)
	case tailnet.ServesService:
		text += "; Serve maps the origin to the service"
	default:
		text += "; Serve fronts " + tailnet.Serve + ", not the service"
	}
	if tailnet.HTTPS == catalog.TailnetHTTPSDisabled {
		text += "; HTTPS certificates disabled on the tailnet"
	}
	if !tailnet.Online {
		text += "; node offline"
	}
	return text
}

// describeToolDevices renders the paired devices of a Tool when they were queried.
func describeToolDevices(devices *catalog.DeviceReport) string {
	switch {
	case devices == nil:
		return ""
	case devices.Error != "":
		return "; paired " + devices.Error
	case len(devices.Items) == 0:
		return "; no paired device"
	}
	names := make([]string, 0, len(devices.Items))
	for _, device := range devices.Items {
		name := device.Client
		if device.Connected {
			name += " (connected)"
		}
		names = append(names, name)
	}
	return "; paired devices: " + strings.Join(names, ", ")
}

// connectTool runs one T3 Connect account action through the manager as agent: after the sandbox
// checks it needs an enabled Tool, and the interactive actions need a terminal before anything changes.
func connectTool(request Request, p podmanTarget, stdout, stderr io.Writer) error {
	names, err := requireAgentManager(request, p)
	if err != nil {
		return err
	}
	status, err := readToolStatus(request, p, "")
	if err != nil {
		return err
	}
	if !status.Enabled {
		return fmt.Errorf("%s not enabled; run tools enable %s %s", request.Args[1], request.Name, request.Args[1])
	}
	tool, err := catalog.Default.ResolveTool(request.Args[1])
	if err != nil {
		return err
	}
	action := request.Args[2]
	interactive := p.host.IsInputTerminal(p.host.Stdin) && p.host.IsOutputTerminal(stdout)
	if slices.Contains(tool.InteractiveConnectActions, action) && !interactive {
		return fmt.Errorf("tools connect %s %s needs an interactive terminal", request.Args[1], action)
	}
	exit, err := p.run(p.host.Stdin, stdout, stderr, agentExecArgs(interactive, names.Container, "tools", "connect", request.Args[1], action)...)
	if err != nil {
		return err
	}
	if podmanTransportFailure(exit) {
		return agentManagerError(request, fmt.Errorf("manager request failed with exit status %d", exit))
	}
	if exit != 0 {
		return fmt.Errorf("tools connect %s %s failed", request.Args[1], action)
	}
	return nil
}

// agentExecArgs builds the Podman arguments that run the manager directly as agent with the caller's
// input, and with a terminal when the caller has one (ADR-0006).
func agentExecArgs(interactive bool, container string, payload ...string) []string {
	args := []string{"exec", "--user=1000:1000", "--env", "HOME=/home/agent", "-i"}
	if interactive {
		args = append(args, "-t")
	}
	return append(append(args, container, managerPath), payload...)
}

// describeToolConnect renders the account connection state of a Tool.
func describeToolConnect(connect *catalog.ConnectStatus) string {
	if connect == nil {
		return ""
	}
	if connect.Error != "" {
		return "; T3 Connect " + connect.Error
	}
	text := "; T3 Connect not signed in"
	if connect.Authenticated {
		text = "; T3 Connect signed in"
		if connect.Account != "" {
			text += " as " + connect.Account
		}
	}
	if connect.Linked {
		text += ", linked"
	} else {
		text += ", not linked"
	}
	if connect.RelayClient != "" {
		text += "; relay client " + connect.RelayClient
	}
	switch {
	case connect.Reachability == "":
	case connect.Reachable:
		text += "; relay reachable"
	default:
		text += "; relay failure: " + connect.Reachability
	}
	return text
}

// describeToolService renders the service state with its last failure and log location.
func describeToolService(status catalog.ToolStatus) string {
	text := "service " + status.ServiceState
	if status.ServiceState != catalog.ServiceRunning && status.ServiceFailure != "" {
		text += ": " + status.ServiceFailure
	}
	if status.ServiceLog != "" {
		text += "; log " + status.ServiceLog
	}
	return text
}

// toolRestoreFailure reports a running sandbox whose Tool services could not be restored; the lifecycle work before it succeeded.
type toolRestoreFailure struct{ cause error }

// Error returns the cause's message.
func (e toolRestoreFailure) Error() string { return e.cause.Error() }

// Unwrap exposes the underlying failure.
func (e toolRestoreFailure) Unwrap() error { return e.cause }

// managerWithoutTools reports whether a manager answered with its usage text and status 2, which a manager
// from before the Tool commands does for a command it does not know. The sandbox then has no Tool, and the
// lifecycle command and the check that asked do not fail for it.
func managerWithoutTools(exit int, diagnostic string) bool {
	return exit == 2 && strings.Contains(diagnostic, "usage: sandboxed-agents-manager")
}

// restoreToolServices asks the manager, as agent, to start the service of every enabled Tool after
// the sandbox is ready; a failure keeps the sandbox running and names tools status, and a manager
// without the Tool commands has nothing to restore.
func restoreToolServices(request Request, p podmanTarget, stdout io.Writer) error {
	var output, diagnostic bytes.Buffer
	exit, err := p.run(nil, &output, &diagnostic, toolManagerArgs(request, "restore")...)
	if err == nil && managerWithoutTools(exit, diagnostic.String()) {
		return nil
	}
	if err != nil || podmanTransportFailure(exit) {
		if err == nil {
			err = fmt.Errorf("manager request failed with exit status %d: %s", exit, strings.TrimSpace(diagnostic.String()))
		}
		return toolRestoreFailure{fmt.Errorf("sandbox %s is running, but its tool services could not be restored: %w; use tools status %s %s", request.Name, agentManagerError(request, err), request.Name, catalog.T3CodeTool)}
	}
	_, outputErr := io.Copy(stdout, &output)
	if exit != 0 {
		return toolRestoreFailure{errors.Join(fmt.Errorf("sandbox %s is running, but a tool service did not start: %s; use tools status %s %s", request.Name, strings.TrimSpace(diagnostic.String()), request.Name, catalog.T3CodeTool), outputErr)}
	}
	return outputErr
}
