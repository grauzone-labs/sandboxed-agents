package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// Request is the parsed command, before any host prerequisite or Podman query.
type Request struct {
	Command         string
	Group           string
	Name            string
	AgentWorkflow   string
	ConfigSelection catalog.ConfigSelection
	OrganizationURL string
	Args            []string
	Options         map[string]string
	Toolchains      []string
	Resources       Resources
}

type commandSpec struct {
	min, max int
	sandbox  bool
	options  map[string]bool // true means the option takes a value
}

// spec builds a command definition from its positional range, sandbox requirement, and option
// names.
func spec(min, max int, sandbox bool, values, switches string) commandSpec {
	options := make(map[string]bool)
	for _, name := range strings.Fields(values) {
		options[name] = true
	}
	for _, name := range strings.Fields(switches) {
		options[name] = false
	}
	return commandSpec{min, max, sandbox, options}
}

var commands = map[string]commandSpec{
	"version":             spec(0, 0, false, "", ""),
	"list":                spec(0, 0, false, "", ""),
	"check":               spec(0, 1, false, "", ""),
	"build":               spec(0, 0, false, "with", ""),
	"up":                  spec(1, 2, true, "with agents port memory cpus pids-limit shm-size", "ssh-config"),
	"start":               spec(1, 1, true, "", "ssh-config"),
	"stop":                spec(1, 1, true, "", "force"),
	"restart":             spec(1, 1, true, "", "force"),
	"remove":              spec(1, 1, true, "", "volumes force"),
	"update":              spec(1, 1, true, "with", "force"),
	"shell":               spec(1, 1, true, "", ""),
	"ssh-config":          spec(1, 1, true, "", "install remove"),
	"fingerprint":         spec(1, 1, true, "", ""),
	"agents enable":       spec(2, 2, true, "version", "force"),
	"agents disable":      spec(2, 2, true, "", "force"),
	"agents update":       spec(2, 2, true, "", "unpin force"),
	"agents login":        spec(2, 3, true, "", ""),
	"agents status":       spec(2, 2, true, "", ""),
	"agents run":          spec(2, -1, true, "", ""),
	"agents session":      spec(2, 2, true, "", "stop"),
	"integrations login":  spec(2, 3, true, catalog.OrganizationOption, ""),
	"integrations config": spec(2, 3, true, "", ""),
	"tools enable":        spec(2, 2, true, "channel version", ""),
	"tools update":        spec(2, 2, true, "channel version", ""),
	"tools status":        spec(2, 2, true, "", ""),
	"tools connect":       spec(3, 3, true, "", ""),
	"tools tailscale":     spec(3, 3, true, "", ""),
	"tools pair":          spec(2, 2, true, "ttl", "revoke"),
	"tools delete":        spec(2, 2, true, "", ""),
}

// leafOptions returns the options a known leaf command accepts before its positionals stage more,
// keyed by name with true for an option that takes a value. integrations config gets a new map of
// the catalog's delivered config options, so staging adds to it without touching commands.
func leafOptions(command string) map[string]bool {
	if command != "integrations config" {
		return commands[command].options
	}
	options := make(map[string]bool)
	for _, option := range catalog.Default.ConfigOptionNames() {
		options[option] = true
	}
	return options
}

// Parse enforces command-first grammar and step 1 checks before any Podman call.
func Parse(args []string, group string) (Request, error) {
	return parseForHost(args, group, Host{}.defaults())
}

// parseForHost performs step-one grammar, catalog, name, and workspace checks using the supplied host facts.
func parseForHost(args []string, group string, host Host) (Request, error) {
	request := Request{Group: group, Options: make(map[string]string), Resources: DefaultResources()}
	if len(args) == 0 {
		return request, fmt.Errorf("a command is required")
	}
	if strings.Contains(args[0], " ") {
		return request, fmt.Errorf("unknown command %q", args[0])
	}
	request.Command = args[0]
	rest := args[1:]
	if request.Command == "agents" || request.Command == "integrations" || request.Command == "tools" {
		if len(rest) == 0 {
			return request, fmt.Errorf("%s requires a subcommand", request.Command)
		}
		request.Command += " " + rest[0]
		rest = rest[1:]
	}
	definition, known := commands[request.Command]
	if !known {
		return request, fmt.Errorf("unknown command %q", request.Command)
	}
	definition.options = leafOptions(request.Command)

	if request.Command == "update" && len(rest) > 0 && strings.HasPrefix(rest[0], "--") && slices.Contains(rest, "--all") {
		definition = spec(0, 0, false, "", "all force")
	}
	if definition.sandbox && (len(rest) == 0 || rest[0] == "" || strings.HasPrefix(rest[0], "-")) {
		return request, fmt.Errorf("sandbox name must follow the command path")
	}
	if request.Command == "agents run" {
		if len(rest) < 2 || rest[1] == "" || strings.HasPrefix(rest[1], "-") {
			return request, fmt.Errorf("agents run requires NAME AGENT")
		}
		request.Args = append([]string(nil), rest...)
	} else {
		positionalsOnly := false
		for index := 0; index < len(rest); index++ {
			arg := rest[index]
			if arg == "--" && !positionalsOnly {
				positionalsOnly = true
				continue
			}
			if !strings.HasPrefix(arg, "-") || positionalsOnly {
				if arg == "" {
					return request, fmt.Errorf("empty argument")
				}
				request.Args = append(request.Args, arg)
				// Config positionals begin with NAME followed by INTEGRATION.
				if request.Command == "integrations config" && len(request.Args) == 2 {
					for _, option := range catalog.Default.StagedConfigOptionNames(arg) {
						definition.options[option] = true
					}
				}
				continue
			}
			name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			needsValue, known := definition.options[name]
			if !strings.HasPrefix(arg, "--") || !known {
				return request, fmt.Errorf("unknown option %q", arg)
			}
			if _, duplicate := request.Options[name]; duplicate {
				return request, fmt.Errorf("duplicate option --%s", name)
			}
			if needsValue {
				if !hasValue {
					index++
					if index >= len(rest) || strings.HasPrefix(rest[index], "--") {
						return request, fmt.Errorf("--%s needs a value", name)
					}
					value = rest[index]
				}
				if value == "" {
					return request, fmt.Errorf("--%s needs a value", name)
				}
			} else {
				if hasValue {
					return request, fmt.Errorf("--%s takes no value", name)
				}
				value = "true"
			}
			request.Options[name] = value
		}
	}
	if len(request.Args) < definition.min || (definition.max >= 0 && len(request.Args) > definition.max) {
		return request, fmt.Errorf("wrong number of arguments for %s", request.Command)
	}
	if definition.sandbox || (request.Command == "check" && len(request.Args) > 0) {
		request.Name = request.Args[0]
	}
	if request.Options["install"] != "" && request.Options["remove"] != "" {
		return request, fmt.Errorf("--install and --remove conflict")
	}
	if strings.HasPrefix(request.Command, "agents ") {
		if _, err := catalog.Default.ResolveAgent(request.Args[1]); err != nil {
			return request, err
		}
		if version := request.Options["version"]; version != "" {
			if err := catalog.ValidatePinVersion(version); err != nil {
				return request, err
			}
		}
	}
	if strings.HasPrefix(request.Command, "tools ") {
		tool, err := catalog.Default.ResolveTool(request.Args[1])
		if err != nil {
			return request, err
		}
		if request.Options["channel"] != "" && request.Options["version"] != "" {
			return request, fmt.Errorf("--channel and --version conflict")
		}
		if channel := request.Options["channel"]; channel != "" {
			if err := tool.ValidateChannel(channel); err != nil {
				return request, err
			}
		}
		if version := request.Options["version"]; version != "" {
			if err := catalog.ValidatePinVersion(version); err != nil {
				return request, err
			}
		}
		if request.Command == "tools connect" {
			if _, err := tool.ResolveConnectAction(request.Args[2]); err != nil {
				return request, err
			}
		}
		if request.Command == "tools tailscale" && !slices.Contains(catalog.TailnetActions, request.Args[2]) {
			return request, fmt.Errorf("unknown tailscale action %q: %s", request.Args[2], catalog.ValidNames(catalog.TailnetActions))
		}
		if request.Command == "tools pair" {
			if request.Options["ttl"] != "" && request.Options["revoke"] != "" {
				return request, fmt.Errorf("--ttl and --revoke conflict")
			}
			if ttl := request.Options["ttl"]; ttl != "" {
				if err := catalog.ValidatePairingTTL(ttl); err != nil {
					return request, err
				}
			}
		}
	}
	switch request.Command {
	case "agents login":
		workflow, err := catalog.Default.ResolveAgentLogin(request.Args[1], loginWorkflow(request.Args))
		if err != nil {
			return request, err
		}
		request.AgentWorkflow = workflow
	case "integrations login":
		if _, err := catalog.Default.ResolveLogin(request.Args[1], loginWorkflow(request.Args)); err != nil {
			return request, err
		}
		integration := catalog.Default.Integrations[request.Args[1]]
		workflow := integration.Login
		for option := range request.Options {
			if _, known := workflow.Options[option]; !known {
				return request, fmt.Errorf("%s", workflow.InvalidOptionsMessage)
			}
		}
		for _, option := range workflow.RequiredOptions {
			if request.Options[option] == "" {
				return request, fmt.Errorf("%s", workflow.InvalidArgumentsMessage)
			}
		}
		if request.Args[1] == catalog.AzureDevOpsIntegration {
			organization, _, err := catalog.AzureDevOpsOrganization(request.Options[catalog.OrganizationOption])
			if err != nil {
				return request, err
			}
			request.OrganizationURL = organization
		}
	case "integrations config":
		workflowName := ""
		if len(request.Args) == 3 {
			workflowName = request.Args[2]
		}
		selection, err := catalog.Default.ResolveConfig(request.Args[1], workflowName)
		if err != nil {
			return request, err
		}
		request.ConfigSelection = selection
		if selection.Integration == catalog.SSHIntegration {
			target, err := catalog.SSHDestination(request.Options[catalog.SSHHostOption], request.Options[catalog.SSHUserOption])
			if err != nil {
				return request, err
			}
			request.Options[catalog.SSHHostOption] = target
		}
		label := selection.Integration + " " + selection.Workflow
		if selection.Workflow == "" {
			// A workflow without a name, such as SSH's, is named by its command.
			label = selection.Integration + " config"
		}
		for option := range request.Options {
			if !slices.Contains(selection.Definition.Options, option) {
				if len(selection.Definition.Options) > 0 {
					return request, fmt.Errorf("%s does not support option %q", label, "--"+option)
				}
				return request, fmt.Errorf("%s takes no options", label)
			}
		}
	}
	for _, option := range []string{"with", "agents"} {
		value, exists := request.Options[option]
		if !exists {
			continue
		}
		names := strings.Split(value, ",")
		validNames, kind := catalog.Default.AgentNames(), "agent"
		if option == "with" {
			validNames, kind = catalog.Default.Toolchains, "toolchain"
			validNames = append(slices.Clone(validNames), "none")
		}
		for _, name := range names {
			if name == "" || (option == "with" && name == "none" && len(names) > 1) {
				return request, fmt.Errorf("invalid --%s selection %q", option, value)
			}
			if option == "with" && name == "none" {
				continue
			}
			if !slices.Contains(validNames, name) {
				if len(validNames) == 0 {
					return request, fmt.Errorf("unknown %s %q: no valid names are available yet", kind, name)
				}
				return request, fmt.Errorf("unknown %s %q: valid names: %s", kind, name, strings.Join(validNames, ", "))
			}
		}
		if option == "with" && value != "none" {
			slices.Sort(names)
			names = slices.Compact(names)
			request.Toolchains = names
		}
	}
	if err := request.Resources.parse(request.Options); err != nil {
		return request, err
	}
	if request.Command != "version" {
		if err := validateNames(request.Group, request.Name); err != nil {
			return request, err
		}
	}
	if request.Command == "up" && len(request.Args) == 2 {
		workspace, err := host.WorkspacePath(request.Args[1])
		if err != nil {
			return request, err
		}
		if host.Platform == "windows" {
			if _, err := windowsWorkspacePath(workspace); err != nil {
				return request, err
			}
		}
		request.Args[1] = workspace
	}
	return request, nil
}
