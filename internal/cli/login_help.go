package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// helpSelection keeps the command topic separate from optional agent login context.
type helpSelection struct{ command, agent, workflow string }

// agentLoginHelpOptions derives general option details and applicability from delivered workflows.
func agentLoginHelpOptions() []helpOption {
	var options []helpOption
	indexes := make(map[string]int)
	owners := make(map[string][]string)
	for _, name := range catalog.Default.AgentNames() {
		agent := catalog.Default.Agents[name]
		for _, workflow := range slices.Sorted(maps.Keys(agent.LoginWorkflows)) {
			for _, option := range agent.LoginWorkflows[workflow].Options {
				if _, seen := indexes[option.Name]; !seen {
					indexes[option.Name] = len(options)
					general := option
					general.Required, general.Conflicts = false, nil
					options = append(options, loginHelpOption(general))
				}
				owners[option.Name] = append(owners[option.Name], name+" "+workflow)
			}
		}
	}
	for index := range options {
		options[index].text += " Applies to: " + strings.Join(owners[options[index].name], ", ") + "."
	}
	return options
}

// loginHelpOption adapts catalog constraints to the common option renderer.
func loginHelpOption(option catalog.AgentLoginOption) helpOption {
	return helpOption{name: option.Name, value: option.Value, text: option.HelpText(), defaultValue: option.Default}
}

// agentLoginHelpSelection selects catalog help without interpreting or looking up NAME.
func agentLoginHelpSelection(args []string) (helpSelection, error) {
	topic := helpSelection{command: "agents login"}
	if len(args) < 2 {
		return topic, nil
	}
	_, err := catalog.Default.ResolveAgent(args[1])
	if err != nil {
		return topic, err
	}
	topic.agent = args[1]
	if len(args) < 3 {
		return topic, nil
	}
	workflow, err := catalog.Default.ResolveAgentLogin(args[1], args[2])
	if err != nil {
		return topic, err
	}
	topic.workflow = workflow
	return topic, nil
}

// entry expands selected login context or returns ordinary command help.
func (selection helpSelection) entry() helpEntry {
	if selection.agent == "" {
		return helpTopics()[selection.command]
	}
	name := selection.agent
	agent := catalog.Default.Agents[name]
	workflows := slices.Sorted(maps.Keys(agent.LoginWorkflows))
	if selection.workflow != "" {
		workflows = []string{selection.workflow}
	}
	entry := helpEntry{summary: "Run a login workflow of " + name + ".", arguments: []helpRow{sandboxArgument}}
	for _, workflow := range workflows {
		definition := agent.LoginWorkflows[workflow]
		form := "agents login NAME " + name + " " + workflow
		section := helpSection{title: workflow + " workflow"}
		for _, option := range definition.Options {
			row := loginHelpOption(option).row()
			form += " [" + row.name + "]"
			section.rows = append(section.rows, row)
		}
		if len(section.rows) == 0 {
			section.rows = []helpRow{{"Options", "No options."}}
		}
		entry.usage = append(entry.usage, form)
		entry.sections = append(entry.sections, section)
		examples := definition.Examples
		if len(examples) == 0 {
			examples = []string{""}
		}
		for _, example := range examples {
			entry.examples = append(entry.examples, strings.TrimSpace(fmt.Sprintf("agents login agent01 %s %s %s", name, workflow, example)))
		}
	}
	return entry
}

// commandHelp returns an executable help command for the current selection.
func (selection helpSelection) commandHelp() string {
	command := "sandboxed-agents"
	if selection.command != "" {
		command += " " + selection.command
	}
	if selection.agent != "" {
		command += " NAME " + selection.agent
	}
	if selection.workflow != "" {
		command += " " + selection.workflow
	}
	return command + " --help"
}
