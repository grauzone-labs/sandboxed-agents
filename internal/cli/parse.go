package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// Request is the parsed command, before any host prerequisite or Podman query.
type Request struct {
	Command           string
	Group             string
	Name              string
	IntegrationConfig catalog.ConfigRequest
	Args              []string
	Options           map[string]string
	Toolchains        []string
	Resources         Resources
}

type commandSpec struct {
	min, max int
	sandbox  bool
	options  map[string]bool // true means the option takes a value
}

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
	"integrations login":  spec(2, 3, true, "organization", ""),
	"integrations config": spec(2, 3, true, "", ""),
}

// Parse enforces command-first grammar and step 1 checks before any Podman call.
func Parse(args []string, group string) (Request, error) {
	return parseForHost(args, group, Host{}.defaults())
}

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
	if request.Command == "agents" || request.Command == "integrations" {
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
	if request.Command == "integrations config" {
		definition = spec(2, 3, true, strings.Join(catalog.Default.ConfigOptionNames(), " "), "")
	}

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
	if strings.HasPrefix(request.Command, "agents ") && !slices.Contains(catalog.Default.Agents, request.Args[1]) {
		if len(catalog.Default.Agents) == 0 {
			return request, fmt.Errorf("unknown agent %q: no valid names are available yet", request.Args[1])
		}
		return request, fmt.Errorf("unknown agent %q: valid names: %s", request.Args[1], strings.Join(catalog.Default.Agents, ", "))
	}
	switch request.Command {
	case "agents login":
		if len(request.Args) == 3 {
			validNames := catalog.Default.AgentLoginWorkflows[request.Args[1]]
			if !slices.Contains(validNames, request.Args[2]) {
				if len(validNames) == 0 {
					return request, fmt.Errorf("unknown workflow %q: no valid names are available yet", request.Args[2])
				}
				return request, fmt.Errorf("unknown workflow %q: valid names: %s", request.Args[2], strings.Join(validNames, ", "))
			}
		}
	case "integrations login":
		integration, known := catalog.Default.Integrations[request.Args[1]]
		if !known || integration.Login == nil {
			if known && len(integration.ConfigWorkflows) > 0 {
				return request, fmt.Errorf("%s has no login workflow; use integrations config %s %s; config workflows: %s", request.Args[1], request.Name, request.Args[1], catalog.ValidNames(integration.ConfigWorkflowNames()))
			}
			return request, fmt.Errorf("unknown integration %q: %s", request.Args[1], catalog.ValidNames(integrationNames(request.Command)))
		}
		if len(request.Args) == 3 && !slices.Contains(integration.LoginWorkflows, request.Args[2]) {
			if len(integration.LoginWorkflows) == 0 {
				return request, fmt.Errorf("unknown workflow %q: no valid names are available yet", request.Args[2])
			}
			return request, fmt.Errorf("unknown workflow %q: valid names: %s", request.Args[2], strings.Join(integration.LoginWorkflows, ", "))
		}
		workflow := integration.Login
		for option := range request.Options {
			if !slices.Contains(workflow.Options, option) {
				return request, fmt.Errorf("%s", workflow.InvalidOptionsMessage)
			}
		}
		if !workflow.WorkflowOmittable && len(request.Args) != 2 {
			return request, fmt.Errorf("%s", workflow.InvalidArgumentsMessage)
		}
		for _, option := range workflow.RequiredOptions {
			if request.Options[option] == "" {
				return request, fmt.Errorf("%s", workflow.InvalidArgumentsMessage)
			}
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
		request.IntegrationConfig = selection
		if len(request.Args) == 2 {
			request.Args = append(request.Args, selection.Workflow)
		}
		for option := range request.Options {
			if !slices.Contains(selection.Definition.Options, option) {
				return request, fmt.Errorf("%s %s takes no options", selection.Integration, selection.Workflow)
			}
		}

	}
	for _, option := range []string{"with", "agents"} {
		value, exists := request.Options[option]
		if !exists {
			continue
		}
		names := strings.Split(value, ",")
		validNames, kind := catalog.Default.Agents, "agent"
		if option == "with" {
			validNames, kind = catalog.Default.Toolchains, "toolchain"
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

func integrationNames(command string) []string {
	if command == "integrations config" {
		return catalog.Default.ConfigIntegrationNames()
	}
	var names []string
	for name, integration := range catalog.Default.Integrations {
		if integration.Login != nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
