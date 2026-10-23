package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// Tailscale programs of the sandbox image, which the base image installs from Tailscale's package repository (ADR-0010).
const (
	tailscaledProgram = "/usr/sbin/tailscaled"
	tailscaleProgram  = "/usr/bin/tailscale"
)

// tailnetHostnameLabel is the form of a node hostname: a DNS label of up to 63 lowercase letters, digits, and inner hyphens.
var tailnetHostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// tailnetDirectory is the `tailscale/` subdirectory of a Tool directory, which holds the tailnet node's state, socket, and supervisor directory.
func tailnetDirectory(directory string) string {
	return filepath.Join(directory, "tailscale")
}

// tailnetStateDirectory holds the node's key, preferences, Serve configuration, and certificates; its presence means the node exists.
func tailnetStateDirectory(directory string) string {
	return filepath.Join(tailnetDirectory(directory), "state")
}

// tailnetSocket is the local API socket of the Tool's tailscaled, which the Tool's own `tailscale` command uses.
func tailnetSocket(directory string) string {
	return filepath.Join(tailnetDirectory(directory), "tailscaled.sock")
}

// tailnetWrapper is the Tool's own `tailscale` command, `bin/tailscale` in the Tool directory: it names the
// node's socket, so that T3 Code's `t3 pair --tailscale`, which calls `tailscale` by name, reaches the Tool's tailscaled.
func tailnetWrapper(directory string) string {
	return filepath.Join(directory, "bin", "tailscale")
}

// tailnetExists reports whether the Tool has a tailnet node: its state directory exists from login until logout or delete.
func tailnetExists(directory string) bool {
	_, err := os.Stat(tailnetStateDirectory(directory))
	return err == nil
}

// tailnetNode is the Tool's supervised tailscaled in userspace networking mode: no TUN device, no
// capability, state and socket inside the Tool directory, and no log upload to Tailscale (ADR-0010).
func tailnetNode(directory, name string) supervised {
	return supervised{kind: supervisedTailnet, tool: name, directory: filepath.Join(tailnetDirectory(directory), "service"), label: name + " tailnet node",
		process: Process{Program: tailscaledProgram, Args: []string{"--tun=userspace-networking", "--statedir=" + tailnetStateDirectory(directory), "--socket=" + tailnetSocket(directory), "--no-logs-no-support"}}}
}

// tailnetInstalled proves that the image has Tailscale by running `tailscale version`; a program that is not installed names the image update.
func tailnetInstalled(name string, effects Effects) error {
	var output, diagnostic bytes.Buffer
	err := effects.Start(Process{Program: tailscaleProgram, Args: []string{"version"}, Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment, Stdout: &output, Stderr: &diagnostic})
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Tailscale is not installed in this sandbox's image; run build, then update NAME, then tools tailscale NAME %s login again", name)
	}
	return fmt.Errorf("tailscale version failed: %s", processFailureSummary(diagnostic.String(), err))
}

// tailnetCommand is the Tool's tailscale CLI call against the node's socket, run as agent.
func tailnetCommand(directory string, args ...string) Process {
	return Process{Program: tailscaleProgram, Args: append([]string{"--socket=" + tailnetSocket(directory)}, args...), Identity: agentIdentity, EnvironmentPolicy: AgentEnvironment}
}

// tailnetOutput runs a tailscale CLI call and returns its standard output, or an error that carries the first line of its diagnostic.
func tailnetOutput(directory string, effects Effects, args ...string) (string, error) {
	return processOutput(tailnetCommand(directory, args...), effects)
}

// writeTailnetWrapper writes the Tool's `tailscale` command, executable by agent only.
func writeTailnetWrapper(directory string) error {
	if err := os.MkdirAll(filepath.Dir(tailnetWrapper(directory)), 0700); err != nil {
		return err
	}
	script := "#!/bin/sh\n# The T3 Code Tool's tailscale command reaches the Tool's own tailscaled (ADR-0010).\nexec " + tailscaleProgram + " --socket=" + tailnetSocket(directory) + " \"$@\"\n"
	return os.WriteFile(tailnetWrapper(directory), []byte(script), 0700)
}

// upstreamTailnetStatus is the part of `tailscale status --json` the Tool reads (ipnstate.Status at Tailscale v1.104.2).
type upstreamTailnetStatus struct {
	BackendState string
	Self         *struct {
		DNSName string
		Online  bool
		CapMap  map[string]json.RawMessage
	}
}

// upstreamServeConfig is the part of `tailscale serve status --json` the Tool reads: the web handlers per host and port.
type upstreamServeConfig struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string
		}
	}
}

// fillTailnetStatus adds the tailnet node to a Tool status: nothing without a node, the supervised
// state always, and upstream's view of the node when asked, which runs the Tool's tailscale command.
func fillTailnetStatus(status *ToolStatus, directory, name string, definition catalog.Tool, query bool, effects Effects) error {
	if !tailnetExists(directory) {
		return nil
	}
	node := tailnetNode(directory, name)
	state, failure, err := reportedState(node.directory, effects)
	if err != nil {
		return err
	}
	status.Tailnet = &catalog.TailnetStatus{Node: state, NodeFailure: failure, NodeLog: node.logPath()}
	if query && state == catalog.ServiceRunning {
		queryTailnet(status.Tailnet, directory, definition, effects)
	}
	return nil
}

// queryTailnet fills the sign-in state, the MagicDNS name, the origin, the capabilities, and the Serve
// mapping of a running node from its own status and Serve configuration, and whether the mapping fronts the
// Tool's service; a failing query is reported in the status.
func queryTailnet(t *catalog.TailnetStatus, directory string, definition catalog.Tool, effects Effects) {
	output, err := tailnetOutput(directory, effects, "status", "--json")
	if err != nil {
		t.Error = "status unavailable: " + err.Error()
		return
	}
	var upstream upstreamTailnetStatus
	if err := json.Unmarshal([]byte(output), &upstream); err != nil {
		t.Error = "status unreadable: " + err.Error()
		return
	}
	t.Backend = upstream.BackendState
	if upstream.Self != nil {
		t.Name = strings.TrimSuffix(strings.TrimSpace(upstream.Self.DNSName), ".")
		t.Online = upstream.Self.Online
		switch _, https := upstream.Self.CapMap["https"]; {
		case upstream.Self.CapMap == nil:
			t.HTTPS = catalog.TailnetHTTPSUnknown
		case https:
			t.HTTPS = catalog.TailnetHTTPSEnabled
		default:
			t.HTTPS = catalog.TailnetHTTPSDisabled
		}
	}
	if t.Backend == catalog.TailnetBackendRunning && t.Name != "" {
		t.Origin = "https://" + t.Name + "/"
	}
	serve, err := tailnetServeTarget(directory, effects)
	if err != nil {
		t.Error = "serve status unavailable: " + err.Error()
		return
	}
	t.Serve, t.ServesService = serve, serve != "" && serve == definition.ServiceOrigin
}

// tailnetServeTarget reads the loopback target that the node's HTTPS port 443 fronts, or nothing without such a mapping.
func tailnetServeTarget(directory string, effects Effects) (string, error) {
	output, err := tailnetOutput(directory, effects, "serve", "status", "--json")
	if err != nil {
		return "", err
	}
	output = strings.TrimSpace(output)
	if !strings.HasPrefix(output, "{") {
		return "", nil
	}
	var config upstreamServeConfig
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return "", fmt.Errorf("serve configuration unreadable: %w", err)
	}
	for hostPort, web := range config.Web {
		if strings.HasSuffix(hostPort, ":443") {
			if handler, found := web.Handlers["/"]; found {
				return handler.Proxy, nil
			}
		}
	}
	return "", nil
}

// tailnetAction runs `tools tailscale TOOL login|logout` under the manager lock: both start or stop the node's supervisor.
func tailnetAction(directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	if _, enabled, err := readToolSelection(directory); err != nil {
		return err
	} else if !enabled {
		return fmt.Errorf("%s not enabled; run tools enable NAME %s", r.name, r.name)
	}
	if r.action == "login" {
		return tailnetLogin(directory, r, stdout, stderr, effects)
	}
	return tailnetLogout(directory, r, stdout, stderr, effects)
}

// tailnetLogin creates the Tool's tailnet node when none exists, starts its supervisor, and signs it in
// with upstream's interactive `tailscale up`, which prints the login URL for the user's browser. A login
// that fails on a node it created removes that node again; an existing node is kept as it was.
func tailnetLogin(directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	if err := tailnetInstalled(r.name, effects); err != nil {
		return err
	}
	existed := tailnetExists(directory)
	node := tailnetNode(directory, r.name)
	cleanup := func() {
		if !existed {
			stopSupervised(node, effects)
			os.RemoveAll(tailnetDirectory(directory))
			os.Remove(tailnetWrapper(directory))
		}
	}
	if err := os.MkdirAll(tailnetStateDirectory(directory), 0700); err != nil {
		return err
	}
	if err := writeTailnetWrapper(directory); err != nil {
		cleanup()
		return err
	}
	if err := startSupervised(node, effects); err != nil {
		cleanup()
		return err
	}
	if existed {
		current := &catalog.TailnetStatus{}
		queryTailnet(current, directory, r.definition, effects)
		if current.Backend == catalog.TailnetBackendRunning && current.Name != "" {
			fmt.Fprintf(stdout, "%s tailnet node already signed in as %s; origin %s\n", r.name, current.Name, current.Origin)
			return nil
		}
	}
	if err := runTailnetUp(directory, r, stdout, stderr, effects); err != nil {
		cleanup()
		return err
	}
	signed := &catalog.TailnetStatus{}
	queryTailnet(signed, directory, r.definition, effects)
	switch {
	case signed.Error != "":
		return fmt.Errorf("%s tailnet node: %s", r.name, signed.Error)
	case signed.Backend != catalog.TailnetBackendRunning:
		return fmt.Errorf("%s tailnet node is not signed in after the login (state %s); run tools tailscale NAME %s login again", r.name, signed.Backend, r.name)
	case signed.Name == "":
		fmt.Fprintf(stdout, "%s tailnet node signed in, but the tailnet has no MagicDNS name; enable MagicDNS at https://login.tailscale.com/admin/dns\n", r.name)
	default:
		fmt.Fprintf(stdout, "%s tailnet node signed in as %s; origin %s\n", r.name, signed.Name, signed.Origin)
	}
	return nil
}

// runTailnetUp runs `tailscale up` with the sandbox's name as the node's hostname and no DNS or route
// changes, on the caller's terminal, and watches for Ctrl+C so an interrupted login is named as such.
func runTailnetUp(directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	interrupts, stop := watchInterrupts(effects)
	defer stop()
	process := tailnetCommand(directory, "up", "--hostname="+r.hostname, "--accept-dns=false", "--accept-routes=false")
	process.Stdin, process.Stdout, process.Stderr = effects.Stdin, stdout, stderr
	if err := effects.Start(process); err != nil {
		return interactiveFailure(r.name+" tailscale login", err, interrupts)
	}
	return nil
}

// tailnetLogout signs the node out as a best effort, stops its supervisor, and removes its state,
// socket, and command wrapper; without a node it says so and exits zero.
func tailnetLogout(directory string, r toolRequest, stdout, stderr io.Writer, effects Effects) error {
	if !tailnetExists(directory) {
		fmt.Fprintf(stdout, "%s tailnet node absent; nothing to sign out\n", r.name)
		return nil
	}
	tailnetSignOut(directory, r.name, stderr, effects)
	if err := removeTailnet(directory, r.name, effects); err != nil {
		return fmt.Errorf("%w; run tools tailscale NAME %s logout again", err, r.name)
	}
	fmt.Fprintf(stdout, "%s tailnet node removed\n", r.name)
	return nil
}

// tailnetSignOut runs `tailscale logout` as a best effort, starting a stopped node for it: a failure is a
// warning, because the local state goes anyway, and the node then stays listed in the admin console until it expires.
func tailnetSignOut(directory, name string, stderr io.Writer, effects Effects) {
	const remains = "the node stays listed in the tailnet admin console until it expires or is removed there"
	if err := startSupervised(tailnetNode(directory, name), effects); err != nil {
		fmt.Fprintf(stderr, "warning: %s tailnet node could not start for its sign-out: %v; %s\n", name, err, remains)
		return
	}
	if _, err := tailnetOutput(directory, effects, "logout"); err != nil {
		fmt.Fprintf(stderr, "warning: %s tailscale logout failed: %v; %s\n", name, err, remains)
	}
}

// removeTailnet stops the node's supervisor and removes the node's directory and command wrapper.
func removeTailnet(directory, name string, effects Effects) error {
	if err := stopSupervised(tailnetNode(directory, name), effects); err != nil {
		return fmt.Errorf("%s tailnet node did not stop: %w", name, err)
	}
	if err := os.RemoveAll(tailnetDirectory(directory)); err != nil {
		return fmt.Errorf("%s tailnet node state could not be removed: %w", name, err)
	}
	if err := os.Remove(tailnetWrapper(directory)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s tailscale command could not be removed: %w", name, err)
	}
	return nil
}
