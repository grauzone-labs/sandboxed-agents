package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// agentLoginHelpOptions derives general login options from the delivered workflows.
func agentLoginHelpOptions() []helpOption {
	var options []helpOption
	seen := make(map[string]bool)
	for _, name := range catalog.Default.AgentNames() {
		agent := catalog.Default.Agents[name]
		for _, workflow := range slices.Sorted(maps.Keys(agent.LoginWorkflows)) {
			for _, option := range agent.LoginWorkflows[workflow].Options {
				if !seen[option.Name] {
					options = append(options, helpOption{name: option.Name, value: option.Value, text: option.Description})
					seen[option.Name] = true
				}
			}
		}
	}
	return options
}

// agentLoginHelpTopic selects catalog help without interpreting or looking up NAME.
func agentLoginHelpTopic(args []string) (string, error) {
	topic := "agents login"
	if len(args) < 2 {
		return topic, nil
	}
	agent, err := catalog.Default.ResolveAgent(args[1])
	if err != nil {
		return topic, err
	}
	topic += " " + args[1]
	if len(args) < 3 {
		return topic, nil
	}
	if _, exists := agent.LoginWorkflows[args[2]]; !exists {
		_, err := catalog.Default.ResolveAgentLogin(args[1], args[2])
		return topic, err
	}
	return topic + " " + args[2], nil
}

// helpTopicEntry expands a selected login topic from workflow definitions or returns ordinary command help.
func helpTopicEntry(topic string) helpEntry {
	words := strings.Fields(topic)
	if len(words) < 3 || words[0] != "agents" || words[1] != "login" {
		return helpTopics()[topic]
	}
	name := words[2]
	agent := catalog.Default.Agents[name]
	workflows := slices.Sorted(maps.Keys(agent.LoginWorkflows))
	if len(words) == 4 {
		workflows = []string{words[3]}
	}
	entry := helpEntry{summary: "Run a login workflow of " + name + ".", arguments: []helpRow{sandboxArgument}}
	for _, workflow := range workflows {
		definition := agent.LoginWorkflows[workflow]
		form := "agents login NAME " + name + " " + workflow
		section := helpSection{title: workflow + " workflow"}
		for _, option := range definition.Options {
			label := "--" + option.Name
			if option.Value != "" {
				label += " " + option.Value
			}
			form += " [" + label + "]"
			text := option.HelpText()
			if option.Default != "" {
				text += " Default: " + option.Default + "."
			}
			section.rows = append(section.rows, helpRow{label, text})
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
