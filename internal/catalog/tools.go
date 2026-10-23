package catalog

import (
	"fmt"
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
}
