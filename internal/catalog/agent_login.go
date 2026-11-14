package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// AgentLoginOption defines an applicable workflow option, its help, and its constraints.
type AgentLoginOption struct {
	Name, Value, Description string
	Required                 bool
	Nonblank                 bool
	RequiredUnless           string
	Conflicts                []string
	Choices                  []string
	Default                  string
}

// AgentLoginWorkflow keeps execution arguments and user-facing login rules together.
type AgentLoginWorkflow struct {
	Description string
	Args        []string
	Options     []AgentLoginOption
	// Examples are option suffixes appended to a concrete agent and workflow command.
	Examples []string
}

// proxyLoginPolicy names the differences between the agents' proxy workflows.
type proxyLoginPolicy struct {
	ModelRequired, ModelNonblank, WireAPI, ReplaceLogin bool
}

// proxyLoginWorkflow defines proxy setup and removal, with each agent's supported additions.
func proxyLoginWorkflow(policy proxyLoginPolicy) AgentLoginWorkflow {
	w := AgentLoginWorkflow{Description: "Configure an existing CLIProxyAPI server for this agent. Setup reads one client API key from the first line of standard input, never as an option or argument. A terminal shows a Key: prompt with input hidden; no terminal is required. --remove reads no key.", Options: []AgentLoginOption{
		{Name: "url", Value: "URL", Description: "Proxy server base URL.", Required: true, RequiredUnless: "remove"},
		{Name: "model", Value: "ID", Description: "Proxy model selection.", Required: policy.ModelRequired, Nonblank: policy.ModelNonblank, RequiredUnless: "remove"},
	}}
	conflicts := []string{"url", "model"}
	if policy.WireAPI {
		w.Options = append(w.Options, AgentLoginOption{Name: "wire-api", Value: "API", Description: "Proxy wire API.", Choices: []string{"completions", "responses"}, Default: "completions"})
		conflicts = append(conflicts, "wire-api")
	}
	if policy.ReplaceLogin {
		w.Options = append(w.Options, AgentLoginOption{Name: "replace-login", Description: "Allow proxy setup to replace the ChatGPT login."})
		conflicts = append(conflicts, "replace-login")
	}
	w.Options = append(w.Options, AgentLoginOption{Name: "remove", Description: "Remove managed proxy settings without reading a key.", Conflicts: conflicts})
	example := "--url https://proxy.example"
	if policy.ModelRequired {
		example += " --model ID"
	}
	w.Examples = []string{example, "--remove"}
	return w
}

// AgentLoginOptionSummary describes a general login option and the workflows that accept it.
type AgentLoginOptionSummary struct {
	Option    AgentLoginOption
	Workflows []string
}

// AgentLoginOptionSummaries returns the stable option union and its owners for help and parsing.
func (c Catalog) AgentLoginOptionSummaries() []AgentLoginOptionSummary {
	var summaries []AgentLoginOptionSummary
	indexes := make(map[string]int)
	for _, name := range c.AgentNames() {
		agent := c.Agents[name]
		for _, workflow := range slices.Sorted(maps.Keys(agent.LoginWorkflows)) {
			for _, option := range agent.LoginWorkflows[workflow].Options {
				index, exists := indexes[option.Name]
				if !exists {
					index = len(summaries)
					indexes[option.Name] = index
					summaries = append(summaries, AgentLoginOptionSummary{Option: option})
				}
				summaries[index].Workflows = append(summaries[index].Workflows, name+" "+workflow)
			}
		}
	}
	return summaries
}

// AgentLoginOptions returns the union of delivered workflow options for token parsing.
func (c Catalog) AgentLoginOptions() map[string]bool {
	options := make(map[string]bool)
	for _, summary := range c.AgentLoginOptionSummaries() {
		options[summary.Option.Name] = summary.Option.Value != ""
	}
	return options
}

// HelpText describes the same requirements, conflicts, and choices that validation enforces.
func (o AgentLoginOption) HelpText() string {
	text := o.Description
	if o.Required {
		text += " Required"
		if o.RequiredUnless != "" {
			text += " unless --" + o.RequiredUnless
		}
		text += "."
	}
	if len(o.Conflicts) > 0 {
		text += " Conflicts with --" + strings.Join(o.Conflicts, ", --") + "."
	}
	if len(o.Choices) > 0 {
		text += " Choices: " + strings.Join(o.Choices, ", ") + "."
	}
	return text
}

// ValidateOptions rejects inapplicable options, conflicts, missing requirements, and invalid choices without effects.
func (w AgentLoginWorkflow) ValidateOptions(options map[string]string) error {
	applicable := w.OptionSyntax()
	for _, name := range slices.Sorted(maps.Keys(options)) {
		if _, known := applicable[name]; !known {
			return fmt.Errorf("workflow does not support --%s", name)
		}
	}
	for _, option := range w.Options {
		if _, present := options[option.Name]; !present {
			continue
		}
		for _, conflict := range option.Conflicts {
			if _, present := options[conflict]; present {
				return fmt.Errorf("--%s conflicts with --%s", option.Name, conflict)
			}
		}
	}
	for _, option := range w.Options {
		value := options[option.Name]
		missing := value == "" || option.Nonblank && strings.TrimSpace(value) == ""
		if option.Required && missing && (option.RequiredUnless == "" || options[option.RequiredUnless] == "") {
			return fmt.Errorf("workflow requires --%s", option.Name)
		}
		if value, present := options[option.Name]; present && len(option.Choices) > 0 && !slices.Contains(option.Choices, value) {
			return fmt.Errorf("--%s requires %s", option.Name, strings.Join(option.Choices, " or "))
		}
	}
	return nil
}

// OptionSyntax returns applicable option names with true for a value and false for a switch.
func (w AgentLoginWorkflow) OptionSyntax() map[string]bool {
	options := make(map[string]bool, len(w.Options))
	for _, option := range w.Options {
		options[option.Name] = option.Value != ""
	}
	return options
}
