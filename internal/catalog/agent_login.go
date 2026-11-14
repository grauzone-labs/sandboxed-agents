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
	RequiredUnless           string
	Conflicts                []string
	Choices                  []string
	Default                  string
}

// AgentLoginWorkflow keeps execution arguments and user-facing login rules together.
type AgentLoginWorkflow struct {
	Args    []string
	Options []AgentLoginOption
	// Examples are option suffixes appended to a concrete agent and workflow command.
	Examples []string
}

// proxyLoginWorkflow defines proxy setup and removal, with each agent's supported additions.
func proxyLoginWorkflow(modelRequired, wireAPI, replaceLogin bool) AgentLoginWorkflow {
	w := AgentLoginWorkflow{Options: []AgentLoginOption{
		{Name: "url", Value: "URL", Description: "Proxy server base URL.", Required: true, RequiredUnless: "remove"},
		{Name: "model", Value: "ID", Description: "Proxy model selection.", Required: modelRequired, RequiredUnless: "remove"},
	}}
	conflicts := []string{"url", "model"}
	if wireAPI {
		w.Options = append(w.Options, AgentLoginOption{Name: "wire-api", Value: "API", Description: "Proxy wire API.", Choices: []string{"completions", "responses"}, Default: "completions"})
		conflicts = append(conflicts, "wire-api")
	}
	if replaceLogin {
		w.Options = append(w.Options, AgentLoginOption{Name: "replace-login", Description: "Allow proxy setup to replace the ChatGPT login."})
		conflicts = append(conflicts, "replace-login")
	}
	w.Options = append(w.Options, AgentLoginOption{Name: "remove", Description: "Remove managed proxy settings without reading a key.", Conflicts: conflicts})
	example := "--url https://proxy.example"
	if modelRequired {
		example += " --model MODEL"
	}
	w.Examples = []string{example, "--remove"}
	return w
}

// AgentLoginOptions returns the union of delivered workflow options for token parsing.
func (c Catalog) AgentLoginOptions() map[string]bool {
	options := make(map[string]bool)
	for _, name := range c.AgentNames() {
		for _, workflow := range c.Agents[name].LoginWorkflows {
			for _, option := range workflow.Options {
				options[option.Name] = option.Value != ""
			}
		}
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
		text += " conflicts with --" + strings.Join(o.Conflicts, ", --") + "."
	}
	if len(o.Choices) > 0 {
		text += " Choices: " + strings.Join(o.Choices, ", ") + "."
	}
	return text
}

// ValidateOptions rejects inapplicable options, conflicts, missing requirements, and invalid choices without effects.
func (w AgentLoginWorkflow) ValidateOptions(options map[string]string) error {
	applicable := make(map[string]bool)
	for _, option := range w.Options {
		applicable[option.Name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(options)) {
		if !applicable[name] {
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
		if option.Required && strings.TrimSpace(options[option.Name]) == "" && (option.RequiredUnless == "" || options[option.RequiredUnless] == "") {
			return fmt.Errorf("workflow requires --%s", option.Name)
		}
		if value, present := options[option.Name]; present && len(option.Choices) > 0 && !slices.Contains(option.Choices, value) {
			return fmt.Errorf("--%s requires %s", option.Name, strings.Join(option.Choices, " or "))
		}
	}
	return nil
}
