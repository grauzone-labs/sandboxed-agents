package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Tool is shared installation data for a Tool that runs beside agents in a sandbox. Undelivered entries are unknown.
type Tool struct {
	Delivered bool
	Install   Install
	// Command is the launcher the Tool's package installs and the command link in the agent's bin directory.
	Command string
	// Channels are the release lines a user may select, each one npm tag of the package; the first is the default channel.
	Channels []string
	// HomeVariable names the environment variable that moves the Tool's own state into its Tool directory.
	HomeVariable string
	// VersionArgs make the launcher download and verify its runtime and print the running version.
	VersionArgs []string
	// VersionPrefix precedes the version in the launcher's version output.
	VersionPrefix string
	// ServiceArgs start the Tool's service in the foreground on the sandbox's loopback.
	ServiceArgs []string
	// ConnectActions maps each account action of `tools connect` to the launcher arguments that run it; interactive actions are listed in InteractiveConnectActions.
	ConnectActions map[string][]string
	// InteractiveConnectActions are the connect actions that need a terminal because upstream shows a URL and a code and waits.
	InteractiveConnectActions []string
	// ConnectStatusArgs print the account connection state as JSON.
	ConnectStatusArgs []string
	// CredentialPath is the path, relative to the Tool's own home, of the account credential that a failed login must not leave behind.
	CredentialPath string
	// ServiceOrigin is the loopback origin the service listens on, which the tailnet node's Serve mapping must front (ADR-0010).
	ServiceOrigin string
	// PairArgs issue a one-time pairing code for the tailnet HTTPS origin and print it as a QR code; TTLOption names the option that sets its validity.
	PairArgs  []string
	TTLOption string
	// PairingListArgs list the unused pairing codes as JSON; PairingRevokeArgs revoke one code, named by its id as the last argument.
	PairingListArgs, PairingRevokeArgs []string
	// SessionListArgs list the paired devices as JSON.
	SessionListArgs []string
}

// ConnectActionNames returns the Tool's connect actions in stable display order.
func (t Tool) ConnectActionNames() []string {
	names := make([]string, 0, len(t.ConnectActions))
	for name := range t.ConnectActions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ResolveConnectAction rejects unknown connect actions and lists the valid ones.
func (t Tool) ResolveConnectAction(action string) ([]string, error) {
	args, known := t.ConnectActions[action]
	if !known {
		return nil, fmt.Errorf("unknown connect action %q: %s", action, ValidNames(t.ConnectActionNames()))
	}
	return args, nil
}

// ConnectStatus is the account connection state of a Tool, as its upstream status command reports it.
type ConnectStatus struct {
	// Authenticated reports whether an account login is stored.
	Authenticated bool `json:"authenticated"`
	// Linked reports whether the sandbox is linked as an environment of the account.
	Linked bool `json:"linked"`
	// Account is the account identifier upstream reports, empty without a login.
	Account string `json:"account,omitempty"`
	// RelayClient is the state of the relay client: available, missing, or unsupported.
	RelayClient string `json:"relay_client,omitempty"`
	// Reachability is upstream's reachability report, as compact JSON, or empty while no server answered.
	Reachability string `json:"reachability,omitempty"`
	// Reachable is true when upstream's report says other devices reach the service; false with a report means a relay failure, whose reason the report holds.
	Reachable bool `json:"reachable,omitempty"`
	// Error is set when the upstream status command could not be run or read.
	Error string `json:"error,omitempty"`
}

// TailnetStatus is the state of a Tool's tailnet node (ADR-0010): the supervised tailscaled, and, when
// upstream's status was queried, the node's sign-in, name, origin, and Serve mapping.
type TailnetStatus struct {
	// Node is running, stopped, or failed, as for the Tool service.
	Node string `json:"node"`
	// NodeFailure is the last failure reason of a node that is not running.
	NodeFailure string `json:"node_failure,omitempty"`
	// NodeLog is the sandbox path of the node's log.
	NodeLog string `json:"node_log,omitempty"`
	// Backend is Tailscale's BackendState: Running once signed in, NeedsLogin before, NeedsMachineAuth while the admin console must approve the node.
	Backend string `json:"backend,omitempty"`
	// Name is the node's MagicDNS name without the trailing dot, empty until signed in or while the tailnet has no MagicDNS.
	Name string `json:"name,omitempty"`
	// Origin is the tailnet HTTPS origin, `https://NAME/`, once the node has a name.
	Origin string `json:"origin,omitempty"`
	// Serve is the loopback target that the node's HTTPS port 443 fronts, or empty without a Serve mapping.
	Serve string `json:"serve,omitempty"`
	// ServesService reports whether that target is the Tool's service origin, so the mapping reaches this sandbox's service.
	ServesService bool `json:"serves_service,omitempty"`
	// Online reports whether the node is connected to the control plane.
	Online bool `json:"online,omitempty"`
	// HTTPS is TailnetHTTPSEnabled or TailnetHTTPSDisabled as the tailnet grants HTTPS certificates, or TailnetHTTPSUnknown when the status carries no capability map.
	HTTPS string `json:"https,omitempty"`
	// Error is set when upstream's status command could not be run or read.
	Error string `json:"error,omitempty"`
}

// DeviceReport lists the paired devices of a Tool, as upstream's session list reports them, or why the list could not be read.
type DeviceReport struct {
	// Items are the device sessions, in upstream's order.
	Items []PairedDevice `json:"items,omitempty"`
	// Error is set when the session list could not be run or read.
	Error string `json:"error,omitempty"`
}

// PairedDevice is a device session of the Tool, as upstream's session list reports it.
type PairedDevice struct {
	// Client names the device from its label, device type, and operating system.
	Client string `json:"client"`
	// Connected reports whether the device is connected now.
	Connected bool `json:"connected"`
	// LastConnected is the time the device last connected, in upstream's ISO form, or empty for never.
	LastConnected string `json:"last_connected,omitempty"`
}

// TailnetBackendRunning is Tailscale's BackendState of a signed-in node; NeedsLogin precedes a login and NeedsMachineAuth waits for the admin console's approval.
const TailnetBackendRunning = "Running"

// Whether the tailnet grants HTTPS certificates, as `tools status` reports it from the node's capability map.
const (
	TailnetHTTPSEnabled  = "enabled"
	TailnetHTTPSDisabled = "disabled"
	TailnetHTTPSUnknown  = "unknown"
)

// PairingPrerequisites is step 8 of the order of checks for `tools pair` (ADR-0009, ADR-0010): from a
// status that carries the tailnet node's sign-in and Serve state it refuses a service that is not running,
// then a node that is absent, not running, unreadable, or not signed in, a tailnet without a MagicDNS name or
// HTTPS certificates, an offline node, and a Serve mapping that fronts another target, each with a message
// that names the fix for the sandbox. The host runs it before the terminal check and the manager again
// before upstream runs, so both sides refuse with the same words.
func PairingPrerequisites(sandbox string, status ToolStatus) error {
	tool := status.Name
	if status.ServiceState != ServiceRunning {
		return fmt.Errorf("%s service is %s; run tools enable %s %s to start it, or tools status %s %s for its failure", tool, status.ServiceState, sandbox, tool, sandbox, tool)
	}
	t := status.Tailnet
	switch {
	case t == nil:
		return fmt.Errorf("%s tailnet node not signed in; run tools tailscale %s %s login", tool, sandbox, tool)
	case t.Node != ServiceRunning:
		return fmt.Errorf("%s tailnet node is %s; run tools enable %s %s to start it, or tools status %s %s for its failure", tool, t.Node, sandbox, tool, sandbox, tool)
	case t.Error != "":
		return fmt.Errorf("%s tailnet node %s", tool, t.Error)
	case t.Backend != TailnetBackendRunning:
		return fmt.Errorf("%s tailnet node not signed in (%s); run tools tailscale %s %s login", tool, t.Backend, sandbox, tool)
	case t.Name == "":
		return fmt.Errorf("the tailnet has no MagicDNS name; enable MagicDNS at https://login.tailscale.com/admin/dns, then run tools pair %s %s again", sandbox, tool)
	case t.HTTPS == TailnetHTTPSDisabled:
		return fmt.Errorf("HTTPS certificates are not enabled on the tailnet; enable them at https://login.tailscale.com/admin/dns, then run tools pair %s %s again", sandbox, tool)
	case !t.Online:
		return fmt.Errorf("%s tailnet node is offline; check the sandbox's outbound connectivity and tools status %s %s", tool, sandbox, tool)
	case t.Serve != "" && !t.ServesService:
		return fmt.Errorf("the tailnet node's HTTPS port 443 already fronts %s, not this service; run tools tailscale %s %s logout and then login to reset the node", t.Serve, sandbox, tool)
	}
	return nil
}

// TailnetActions are the actions of `tools tailscale`: `login` creates and signs in the Tool's tailnet node, `logout` signs it out and removes it (ADR-0010).
var TailnetActions = []string{"login", "logout"}

// pairingTTL is the form of a pairing code's validity: a number with one of upstream's units.
var pairingTTL = regexp.MustCompile(`^[0-9]+(ms|s|m|h|d|w)$`)

// ValidatePairingTTL accepts a pairing code validity such as `5m`, `90s`, or `1h`; upstream's spelled-out form is not accepted on the command line.
func ValidatePairingTTL(ttl string) error {
	if !pairingTTL.MatchString(ttl) {
		return fmt.Errorf("invalid --ttl %q: use a number with ms, s, m, h, d, or w", ttl)
	}
	return nil
}

// Service states of a Tool service, as `tools status` and `check NAME` report them.
const (
	ServiceRunning = "running"
	ServiceStopped = "stopped"
	ServiceFailed  = "failed"
)

// T3CodeTool is the tool name of T3 Code, the first Tool.
const T3CodeTool = "t3code"

// ToolNames returns delivered Tools in stable display order.
func (c Catalog) ToolNames() []string {
	names := make([]string, 0, len(c.Tools))
	for name, tool := range c.Tools {
		if tool.Delivered {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// ResolveTool rejects unknown and undelivered Tools before system operations.
func (c Catalog) ResolveTool(name string) (Tool, error) {
	tool, exists := c.Tools[name]
	if !exists || !tool.Delivered {
		return Tool{}, fmt.Errorf("unknown tool %q: %s", name, ValidNames(c.ToolNames()))
	}
	return tool, nil
}

// DefaultChannel is the channel a first enable without a selector records.
func (t Tool) DefaultChannel() string {
	if len(t.Channels) == 0 {
		return ""
	}
	return t.Channels[0]
}

// ValidateChannel accepts exactly the Tool's channels.
func (t Tool) ValidateChannel(channel string) error {
	if !slices.Contains(t.Channels, channel) {
		return fmt.Errorf("invalid --channel %q: valid channels: %s", channel, strings.Join(t.Channels, ", "))
	}
	return nil
}

// ToolStatus is the shared machine-readable view of a Tool selection and its service.
type ToolStatus struct {
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	InstalledVersion string `json:"installed_version"`
	Channel          string `json:"channel"`
	Pin              string `json:"pin"`
	// ServiceState is running, stopped, or failed for an enabled Tool.
	ServiceState string `json:"service_state,omitempty"`
	// ServiceFailure is the last failure reason of a service that is not running.
	ServiceFailure string `json:"service_failure,omitempty"`
	// ServiceLog is the sandbox path of the service log.
	ServiceLog string `json:"service_log,omitempty"`
	// Connect is the account connection state of an enabled Tool.
	Connect *ConnectStatus `json:"connect,omitempty"`
	// Tailnet is the tailnet node of an enabled Tool, absent while no node exists.
	Tailnet *TailnetStatus `json:"tailnet,omitempty"`
	// Devices are the paired devices of an enabled Tool, present when upstream's session list was queried.
	Devices *DeviceReport `json:"devices,omitempty"`
}
