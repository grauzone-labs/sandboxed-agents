package cli

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// commandGroups lists, in display order, the command words that take a subcommand.
var commandGroups = []string{"agents", "integrations", "tools"}

// topLevelCommands lists the commands outside the groups in display order.
var topLevelCommands = []string{"version", "list", "check", "build", "up", "start", "stop", "restart", "remove", "update", "shell", "ssh-config", "fingerprint"}

// groupSubcommands lists each group's subcommands in display order.
var groupSubcommands = map[string][]string{
	"agents":       {"enable", "disable", "update", "login", "status", "run", "session"},
	"integrations": {"login", "config"},
	"tools":        {"enable", "update", "status", "connect", "tailscale", "pair", "delete"},
}

// helpEntry is the help of one level of the command chain: the root (""), a group, or a leaf
// command. Usage forms and examples omit the leading "sandboxed-agents ".
type helpEntry struct {
	summary   string
	usage     []string
	arguments []helpRow
	options   []helpOption
	sections  []helpSection
	examples  []string
	footer    string
}

// helpRow is one named line of a help section.
type helpRow struct{ name, text string }

// helpOption describes one option of a leaf command; value is empty for a switch.
type helpOption struct{ name, value, text, defaultValue string }

// helpSection is a titled list of rows, such as a group's subcommands or catalog values.
type helpSection struct {
	title string
	rows  []helpRow
}

// sandboxArgument describes the NAME argument shared by every command that acts on a sandbox.
var sandboxArgument = helpRow{"NAME", "The sandbox name."}

// forceSessions describes --force of the commands that end agent sessions.
var forceSessions = helpOption{name: "force", text: "Proceed although agent sessions are running, and end them."}

// helpTopics returns the help of every level of the command chain, keyed by command path, with
// catalog values read from catalog.Default at call time.
func helpTopics() map[string]helpEntry {
	agents := strings.Join(catalog.Default.AgentNames(), ", ")
	toolchains := strings.Join(catalog.Default.Toolchains, ", ")
	agentArgument := helpRow{"AGENT", "An agent of the agent catalog: " + agents + "."}
	toolArgument := helpRow{"TOOL", "A Tool of the Tool catalog: " + strings.Join(catalog.Default.ToolNames(), ", ") + "."}
	channel := helpOption{name: "channel", value: "CHANNEL", text: "Record a channel of the Tool; see Channels."}
	pinVersion := helpOption{name: "version", value: "X", text: "Install exactly this version and record it as a Tool pin."}
	topics := map[string]helpEntry{
		"version": {
			summary:  "Print the version and the hash of the embedded build assets.",
			usage:    []string{"version"},
			examples: []string{"version"},
		},
		"list": {
			summary:  "List the sandboxes of the controller group with state, port, toolchain set, agents, and problems.",
			usage:    []string{"list"},
			examples: []string{"list"},
		},
		"check": {
			summary:   "Check the host prerequisites, or diagnose one sandbox.",
			usage:     []string{"check [NAME]"},
			arguments: []helpRow{{"NAME", "A sandbox to diagnose. Without it, check the host prerequisites."}},
			examples:  []string{"check", "check agent01"},
		},
		"build": {
			summary: "Build the base image and every existing toolchain set anew.",
			usage:   []string{"build [--with SET]"},
			options: []helpOption{
				{name: "with", value: "SET", text: "Also build this toolchain set: comma-separated names of " + toolchains + "."},
			},
			examples: []string{"build", "build --with dotnet,playwright"},
		},
		"up": {
			summary: "Create and start a sandbox, or start an existing one.",
			usage:   []string{"up NAME [WORKSPACE] [--with SET] [--agents LIST] [--port N] [--ssh-config] [--memory SIZE] [--cpus N] [--pids-limit N] [--shm-size SIZE]"},
			arguments: []helpRow{
				{"NAME", "The sandbox name. No name is reserved."},
				{"WORKSPACE", "A host directory to bind at /workspace. Without it, the workspace is a volume of the sandbox."},
			},
			options: []helpOption{
				{name: "with", value: "SET", text: "Toolchain set: comma-separated names of " + toolchains + ", or none alone for the base image.", defaultValue: "none"},
				{name: "agents", value: "LIST", text: "Agents to enable once the sandbox runs: comma-separated names of " + agents + "."},
				{name: "port", value: "N", text: "Host loopback port for SSH.", defaultValue: strconv.Itoa(defaultSSHPort) + ", or the next port no sandbox has recorded"},
				{name: "ssh-config", text: "Install the opt-in SSH setup once the sandbox runs."},
				{name: "memory", value: "SIZE", text: "Memory limit; the sandbox gets no swap.", defaultValue: "8g"},
				{name: "cpus", value: "N", text: "CPU limit.", defaultValue: "4"},
				{name: "pids-limit", value: "N", text: "Limit on processes.", defaultValue: "2048"},
				{name: "shm-size", value: "SIZE", text: "Size of /dev/shm.", defaultValue: "1g"},
			},
			examples: []string{"up agent01", "up agent01 --with dotnet --agents claude,codex --memory 12g"},
			footer:   "SIZE is a whole number of bytes with an optional k, m, g, or t suffix. Options apply when the sandbox is created.",
		},
		"start": {
			summary:   "Start a stopped sandbox.",
			usage:     []string{"start NAME [--ssh-config]"},
			arguments: []helpRow{sandboxArgument},
			options:   []helpOption{{name: "ssh-config", text: "Install the opt-in SSH setup once the sandbox runs."}},
			examples:  []string{"start agent01"},
		},
		"stop": {
			summary:   "Stop a running sandbox.",
			usage:     []string{"stop NAME [--force]"},
			arguments: []helpRow{sandboxArgument},
			options:   []helpOption{forceSessions},
			examples:  []string{"stop agent01"},
		},
		"restart": {
			summary:   "Stop and start a sandbox.",
			usage:     []string{"restart NAME [--force]"},
			arguments: []helpRow{sandboxArgument},
			options:   []helpOption{forceSessions},
			examples:  []string{"restart agent01"},
		},
		"remove": {
			summary:   "Remove a sandbox's container and keep its volumes for a later up.",
			usage:     []string{"remove NAME [--volumes] [--force]"},
			arguments: []helpRow{sandboxArgument},
			options: []helpOption{
				{name: "volumes", text: "Also remove the sandbox's volumes with its home data, credentials, and SSH server state."},
				forceSessions,
			},
			examples: []string{"remove agent01", "remove agent01 --volumes"},
		},
		"update": {
			summary:   "Move a sandbox, or every sandbox, to the image of this executable.",
			usage:     []string{"update NAME [--with SET] [--force]", "update --all [--force]"},
			arguments: []helpRow{sandboxArgument},
			options: []helpOption{
				{name: "with", value: "SET", text: "Replace the recorded toolchain set: comma-separated names of " + toolchains + ", or none alone.", defaultValue: "the recorded set"},
				{name: "all", text: "Update every sandbox of the controller group instead of one."},
				forceSessions,
			},
			examples: []string{"update agent01", "update --all"},
		},
		"shell": {
			summary:   "Open a shell as agent in /workspace of a sandbox.",
			usage:     []string{"shell NAME"},
			arguments: []helpRow{sandboxArgument},
			examples:  []string{"shell agent01"},
		},
		"ssh-config": {
			summary:   "Print the host SSH entry of a sandbox, or install or remove the SSH setup.",
			usage:     []string{"ssh-config NAME [--install|--remove]"},
			arguments: []helpRow{sandboxArgument},
			options: []helpOption{
				{name: "install", text: "Create the sandbox's key, authorize it, pin the host key, and write the host entry."},
				{name: "remove", text: "Remove the host side of the SSH setup and the authorization in the sandbox."},
			},
			examples: []string{"ssh-config agent01", "ssh-config agent01 --install"},
		},
		"fingerprint": {
			summary:   "Print the SSH host key fingerprints of a sandbox.",
			usage:     []string{"fingerprint NAME"},
			arguments: []helpRow{sandboxArgument},
			examples:  []string{"fingerprint agent01"},
		},
		"agents enable": {
			summary:   "Install an agent into a sandbox's home data and enable it.",
			usage:     []string{"agents enable NAME AGENT [--version X] [--force]"},
			arguments: []helpRow{sandboxArgument, agentArgument},
			options: []helpOption{
				{name: "version", value: "X", text: "Install exactly this npm package version and pin the agent to it."},
				{name: "force", text: "Repair an interrupted installation, or proceed although the agent's session runs and end it."},
			},
			examples: []string{"agents enable agent01 claude", "agents enable agent01 codex --version 1.2.3"},
		},
		"agents disable": {
			summary:   "Remove an agent's command, selection entry, and pin, and keep its credentials.",
			usage:     []string{"agents disable NAME AGENT [--force]"},
			arguments: []helpRow{sandboxArgument, agentArgument},
			options:   []helpOption{{name: "force", text: "Proceed although the agent's session runs, and end it."}},
			examples:  []string{"agents disable agent01 claude"},
		},
		"agents update": {
			summary:   "Reinstall a pinned agent at its pin, or update an unpinned one.",
			usage:     []string{"agents update NAME AGENT [--unpin] [--force]"},
			arguments: []helpRow{sandboxArgument, agentArgument},
			options: []helpOption{
				{name: "unpin", text: "Install the newest version and clear the pin."},
				{name: "force", text: "Proceed although the agent's session runs, and end it."},
			},
			examples: []string{"agents update agent01 claude", "agents update agent01 codex --unpin"},
		},
		"agents login": {
			summary: "Run one of an agent's login workflows in an interactive terminal.",
			usage:   []string{"agents login NAME AGENT [WORKFLOW]"},
			arguments: []helpRow{sandboxArgument, agentArgument,
				{"WORKFLOW", "A login workflow of the agent; see Login workflows. It may be omitted when the agent has one."}},
			sections: []helpSection{agentLoginWorkflows()},
			examples: []string{"agents login agent01 claude", "agents login agent01 codex device-code"},
		},
		"agents status": {
			summary:   "Report whether an agent is enabled, its version and pin, its session, and its sign-in.",
			usage:     []string{"agents status NAME AGENT"},
			arguments: []helpRow{sandboxArgument, agentArgument},
			examples:  []string{"agents status agent01 claude"},
		},
		"agents run": {
			summary: "Run an agent once in /workspace; the exit status is the agent's.",
			usage:   []string{"agents run NAME AGENT [ARG...]"},
			arguments: []helpRow{sandboxArgument, agentArgument,
				{"ARG", "Passed on to the agent unchanged, --help and -h included."}},
			examples: []string{"agents run agent01 claude", "agents run agent01 codex --help"},
		},
		"agents session": {
			summary:   "Start or attach to an agent's persistent terminal session.",
			usage:     []string{"agents session NAME AGENT [--stop]"},
			arguments: []helpRow{sandboxArgument, agentArgument},
			options:   []helpOption{{name: "stop", text: "End the session; this needs no terminal."}},
			examples:  []string{"agents session agent01 claude", "agents session agent01 claude --stop"},
		},
		"integrations login": {
			summary: "Sign in to an integration's account in a sandbox.",
			usage:   []string{"integrations login NAME INTEGRATION [WORKFLOW] [--OPTION VALUE]"},
			arguments: []helpRow{sandboxArgument,
				{"INTEGRATION", "An integration with a login workflow; see Integrations."},
				{"WORKFLOW", "A login workflow of the integration. It may be omitted when the integration has one or none."}},
			options:  integrationLoginOptions(),
			sections: []helpSection{integrationLogins()},
			examples: []string{"integrations login agent01 github", "integrations login agent01 azdo --organization https://dev.azure.com/contoso"},
		},
		"integrations config": {
			summary: "Run an integration's config workflow in a sandbox.",
			usage:   []string{"integrations config NAME INTEGRATION [WORKFLOW] [--OPTION VALUE]..."},
			arguments: []helpRow{sandboxArgument,
				{"INTEGRATION", "An integration with a config workflow; see Integrations."},
				{"WORKFLOW", "A config workflow of the integration. An integration with one unnamed workflow takes none."}},
			sections: []helpSection{integrationConfigs()},
			examples: []string{"integrations config agent01 git identity --name Ada --email ada@example.com", "integrations config agent01 ssh --host build.example.com --user ada"},
		},
		"tools enable": {
			summary:   "Install a Tool into a sandbox's home data and start its service.",
			usage:     []string{"tools enable NAME TOOL [--channel CHANNEL] [--version X]"},
			arguments: []helpRow{sandboxArgument, toolArgument},
			options:   []helpOption{channel, pinVersion},
			sections:  []helpSection{toolChannels()},
			examples:  []string{"tools enable agent01 t3code", "tools enable agent01 t3code --channel nightly"},
		},
		"tools update": {
			summary:   "Move a Tool to the newest release of its channel, or switch its channel or version.",
			usage:     []string{"tools update NAME TOOL [--channel CHANNEL] [--version X]"},
			arguments: []helpRow{sandboxArgument, toolArgument},
			options:   []helpOption{channel, pinVersion},
			sections:  []helpSection{toolChannels()},
			examples:  []string{"tools update agent01 t3code", "tools update agent01 t3code --version 1.2.3"},
		},
		"tools status": {
			summary:   "Report a Tool's selection, service, account connection, tailnet node, and paired devices.",
			usage:     []string{"tools status NAME TOOL"},
			arguments: []helpRow{sandboxArgument, toolArgument},
			examples:  []string{"tools status agent01 t3code"},
		},
		"tools connect": {
			summary: "Run an account action of a Tool, such as signing in to T3 Connect.",
			usage:   []string{"tools connect NAME TOOL ACTION"},
			arguments: []helpRow{sandboxArgument, toolArgument,
				{"ACTION", "An account action of the Tool; see Actions."}},
			sections: []helpSection{toolConnectActions()},
			examples: []string{"tools connect agent01 t3code login"},
		},
		"tools tailscale": {
			summary: "Sign a Tool's tailnet node in to your tailnet, or sign it out and remove it.",
			usage:   []string{"tools tailscale NAME TOOL ACTION"},
			arguments: []helpRow{sandboxArgument, toolArgument,
				{"ACTION", "A tailnet node action: " + strings.Join(catalog.TailnetActions, ", ") + "."}},
			examples: []string{"tools tailscale agent01 t3code login"},
		},
		"tools pair": {
			summary:   "Issue a one-time pairing code over a Tool's tailnet node, or revoke unused codes.",
			usage:     []string{"tools pair NAME TOOL [--ttl DURATION] [--revoke]"},
			arguments: []helpRow{sandboxArgument, toolArgument},
			options: []helpOption{
				{name: "ttl", value: "DURATION", text: "Validity of the code: a number with ms, s, m, h, d, or w.", defaultValue: "5m"},
				{name: "revoke", text: "Revoke every unused pairing code."},
			},
			examples: []string{"tools pair agent01 t3code", "tools pair agent01 t3code --ttl 10m"},
		},
		"tools delete": {
			summary:   "Remove a Tool and everything it stored from a sandbox.",
			usage:     []string{"tools delete NAME TOOL"},
			arguments: []helpRow{sandboxArgument, toolArgument},
			examples:  []string{"tools delete agent01 t3code"},
		},
	}
	topics["agents"] = groupEntry(topics, "agents", "Enable, update, sign in, and run agents in a sandbox.", "agents SUBCOMMAND NAME AGENT [ARG...]")
	topics["integrations"] = groupEntry(topics, "integrations", "Sign in to and configure integrations in a sandbox.", "integrations SUBCOMMAND NAME INTEGRATION [WORKFLOW] [OPTION...]")
	topics["tools"] = groupEntry(topics, "tools", "Install, update, connect, pair, and remove Tools in a sandbox.", "tools SUBCOMMAND NAME TOOL [ACTION] [OPTION...]")
	root := helpSection{title: "Commands"}
	for _, command := range topLevelCommands {
		root.rows = append(root.rows, helpRow{command, topics[command].summary})
	}
	for _, group := range commandGroups {
		root.rows = append(root.rows, helpRow{group, topics[group].summary})
	}
	topics[""] = helpEntry{
		summary:  "sandboxed-agents runs coding agents in rootless Podman sandboxes.",
		usage:    []string{"COMMAND [ARG...]", "help [COMMAND [SUBCOMMAND]]"},
		sections: []helpSection{root, {"Options", []helpRow{{"-h, --help", "Show the help of the root, a group, or a command."}}}},
		footer:   "Run 'sandboxed-agents COMMAND --help' or 'sandboxed-agents help COMMAND' for the help of a command.",
	}
	return topics
}

// groupEntry builds a group's help from the summaries of its subcommands.
func groupEntry(topics map[string]helpEntry, group, summary, usage string) helpEntry {
	section := helpSection{title: "Subcommands"}
	for _, subcommand := range groupSubcommands[group] {
		section.rows = append(section.rows, helpRow{subcommand, topics[group+" "+subcommand].summary})
	}
	return helpEntry{
		summary:  summary,
		usage:    []string{usage},
		sections: []helpSection{section},
		footer:   "Run 'sandboxed-agents " + group + " SUBCOMMAND --help' for the help of a subcommand.",
	}
}

// agentLoginWorkflows lists each catalog agent with its login workflows.
func agentLoginWorkflows() helpSection {
	section := helpSection{title: "Login workflows"}
	for _, name := range catalog.Default.AgentNames() {
		workflows := slices.Sorted(maps.Keys(catalog.Default.Agents[name].LoginWorkflows))
		section.rows = append(section.rows, helpRow{name, strings.Join(workflows, ", ")})
	}
	return section
}

// integrationLoginOptions returns the options of the catalog's login workflows, each with its
// value label and the integrations it belongs to.
func integrationLoginOptions() []helpOption {
	owners := map[string][]string{}
	labels := map[string]string{}
	for _, name := range catalog.Default.LoginIntegrationNames() {
		for option, label := range catalog.Default.Integrations[name].Login.Options {
			owners[option] = append(owners[option], name)
			labels[option] = label
		}
	}
	var options []helpOption
	for _, option := range slices.Sorted(maps.Keys(owners)) {
		options = append(options, helpOption{name: option, value: labels[option], text: "Option of the " + strings.Join(owners[option], ", ") + " login; see Integrations."})
	}
	return options
}

// integrationLogins lists each catalog integration with a login, with its workflows and options.
func integrationLogins() helpSection {
	section := helpSection{title: "Integrations"}
	for _, name := range catalog.Default.LoginIntegrationNames() {
		integration := catalog.Default.Integrations[name]
		var parts []string
		switch len(integration.LoginWorkflows) {
		case 0:
			parts = append(parts, "no workflow")
		case 1:
			parts = append(parts, "workflow: "+integration.LoginWorkflows[0])
		default:
			parts = append(parts, "workflows: "+strings.Join(integration.LoginWorkflows, ", "))
		}
		for _, option := range slices.Sorted(maps.Keys(integration.Login.Options)) {
			form := "--" + option + " " + integration.Login.Options[option]
			if slices.Contains(integration.Login.RequiredOptions, option) {
				form += " (required)"
			}
			parts = append(parts, form)
		}
		section.rows = append(section.rows, helpRow{name, strings.Join(parts, "; ")})
	}
	return section
}

// integrationConfigs lists each catalog integration with a config workflow, one row per workflow
// with its options.
func integrationConfigs() helpSection {
	section := helpSection{title: "Integrations"}
	for _, name := range catalog.Default.ConfigIntegrationNames() {
		integration := catalog.Default.Integrations[name]
		for _, workflow := range integration.ConfigWorkflowNames() {
			var options []string
			for _, option := range integration.ConfigWorkflows[workflow].Options {
				options = append(options, "--"+option+" VALUE")
			}
			text := "no options"
			if len(options) > 0 {
				text = strings.Join(options, ", ")
			}
			section.rows = append(section.rows, helpRow{strings.TrimSpace(name + " " + workflow), text})
		}
	}
	return section
}

// toolChannels lists each catalog Tool with its channels, marking the default one.
func toolChannels() helpSection {
	section := helpSection{title: "Channels"}
	for _, name := range catalog.Default.ToolNames() {
		tool := catalog.Default.Tools[name]
		var channels []string
		for _, channel := range tool.Channels {
			if channel == tool.DefaultChannel() {
				channel += " (default)"
			}
			channels = append(channels, channel)
		}
		section.rows = append(section.rows, helpRow{name, strings.Join(channels, ", ")})
	}
	return section
}

// toolConnectActions lists each catalog Tool with its account actions.
func toolConnectActions() helpSection {
	section := helpSection{title: "Actions"}
	for _, name := range catalog.Default.ToolNames() {
		section.rows = append(section.rows, helpRow{name, strings.Join(catalog.Default.Tools[name].ConnectActionNames(), ", ")})
	}
	return section
}

// helpFlag reports whether arg is one of the help options.
func helpFlag(arg string) bool {
	return arg == "--help" || arg == "-h"
}

// commandPath returns the deepest help topic that the leading words name, the root being "", and
// the words after it.
func commandPath(words []string) (string, []string) {
	if len(words) == 0 {
		return "", words
	}
	if slices.Contains(commandGroups, words[0]) {
		if len(words) > 1 {
			if _, known := commands[words[0]+" "+words[1]]; known {
				return words[0] + " " + words[1], words[2:]
			}
		}
		return words[0], words[1:]
	}
	if _, known := commands[words[0]]; known {
		return words[0], words[1:]
	}
	return "", words
}

// requestedHelp reports whether args ask for help and for which topic. The root and the groups
// take --help, -h, and help [COMMAND [SUBCOMMAND]]; a leaf takes --help or -h before any "--",
// and agents run only before its AGENT argument, so that later arguments reach the agent. A help
// command that names no known topic returns the deepest recognised topic and an error.
func requestedHelp(args []string) (string, bool, error) {
	if len(args) > 0 && args[0] == "help" {
		return helpCommandTopic(args[1:])
	}
	topic, rest := commandPath(args)
	if len(rest) == 0 {
		return "", false, nil
	}
	if topic == "" {
		return "", helpFlag(rest[0]), nil
	}
	if slices.Contains(commandGroups, topic) {
		if rest[0] == "help" {
			return helpCommandTopic(append([]string{topic}, rest[1:]...))
		}
		return topic, helpFlag(rest[0]), nil
	}
	if topic == "agents run" {
		rest = rest[:min(2, len(rest))]
	}
	for _, arg := range rest {
		if arg == "--" {
			break
		}
		if helpFlag(arg) {
			return topic, true, nil
		}
	}
	return "", false, nil
}

// helpCommandTopic resolves the words after a help command to a topic; a trailing help option is
// allowed, and other words that name no topic are an unknown command.
func helpCommandTopic(words []string) (string, bool, error) {
	topic, rest := commandPath(words)
	if len(rest) == 0 || (len(rest) == 1 && helpFlag(rest[0])) {
		return topic, true, nil
	}
	return topic, true, fmt.Errorf("unknown command %q", strings.Join(words, " "))
}

// usageTopic returns the deepest help topic a parsed command path reached, the root being "".
func usageTopic(command string) string {
	topic, _ := commandPath(strings.Fields(command))
	return topic
}

// writeHelp writes the full help of a topic: description, usage, sections, options, and examples.
func writeHelp(w io.Writer, topic string) {
	entry := helpTopics()[topic]
	fmt.Fprintf(w, "%s\n\n", entry.summary)
	writeUsageLines(w, entry)
	if len(entry.arguments) > 0 {
		writeSection(w, helpSection{"Arguments", entry.arguments})
	}
	if len(entry.options) > 0 {
		var rows []helpRow
		for _, option := range entry.options {
			name := "--" + option.name
			if option.value != "" {
				name += " " + option.value
			}
			text := option.text
			if option.defaultValue != "" {
				text = strings.TrimSuffix(text, ".") + " (default " + option.defaultValue + ")."
			}
			rows = append(rows, helpRow{name, text})
		}
		writeSection(w, helpSection{"Options", rows})
	}
	for _, section := range entry.sections {
		writeSection(w, section)
	}
	if len(entry.examples) > 0 {
		title := "Example:"
		if len(entry.examples) > 1 {
			title = "Examples:"
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, example := range entry.examples {
			fmt.Fprintf(w, "  sandboxed-agents %s\n", example)
		}
	}
	if entry.footer != "" {
		fmt.Fprintf(w, "\n%s\n", entry.footer)
	}
}

// writeUsage writes the usage of a topic after a usage error: the usage lines, the command list of
// the root or a group, and the help command that shows the full help.
func writeUsage(w io.Writer, topic string) {
	entry := helpTopics()[topic]
	writeUsageLines(w, entry)
	if topic == "" || slices.Contains(commandGroups, topic) {
		for _, section := range entry.sections {
			writeSection(w, section)
		}
		fmt.Fprintln(w)
	}
	command := "sandboxed-agents --help"
	if topic != "" {
		command = "sandboxed-agents " + topic + " --help"
	}
	fmt.Fprintf(w, "run '%s' for usage\n", command)
}

// writeUsageLines writes each usage form, the first after "usage:" and the rest aligned below it.
func writeUsageLines(w io.Writer, entry helpEntry) {
	for index, form := range entry.usage {
		prefix := "       "
		if index == 0 {
			prefix = "usage: "
		}
		fmt.Fprintf(w, "%ssandboxed-agents %s\n", prefix, form)
	}
}

// writeSection writes a blank line, the section title, and its rows with aligned descriptions.
func writeSection(w io.Writer, section helpSection) {
	width := 0
	for _, row := range section.rows {
		width = max(width, len(row.name))
	}
	fmt.Fprintf(w, "\n%s:\n", section.title)
	for _, row := range section.rows {
		fmt.Fprintf(w, "  %-*s  %s\n", width, row.name, row.text)
	}
}
