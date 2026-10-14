package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	GitIntegration         = "git"
	GitIdentityWorkflow    = "identity"
	GitCredentialsWorkflow = "credentials"
	GitNameOption          = "name"
	GitEmailOption         = "email"
)

// GitSetting is a global Git value, either fixed or supplied by a workflow option.
type GitSetting struct{ Key, Value, Option string }

// ConfigSelection identifies a resolved config workflow and its shared definition.
type ConfigSelection struct {
	Integration, Workflow string
	Definition            ConfigWorkflow
}

// ConfigWorkflowNames returns the delivered config workflows in display order.
func (i Integration) ConfigWorkflowNames() []string {
	return slices.Sorted(maps.Keys(i.ConfigWorkflows))
}

// ConfigIntegrationNames returns integrations that have delivered config workflows.
func (c Catalog) ConfigIntegrationNames() []string {
	var names []string
	for name, integration := range c.Integrations {
		if len(integration.ConfigWorkflows) > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// ResolveConfig validates delivered names and resolves an omitted single workflow.
func (c Catalog) ResolveConfig(integrationName, workflowName string) (ConfigSelection, error) {
	selection := ConfigSelection{Integration: integrationName, Workflow: workflowName}
	integration, known := c.Integrations[integrationName]
	if !known || len(integration.ConfigWorkflows) == 0 {
		return selection, fmt.Errorf("unknown integration %q: %s", integrationName, ValidNames(c.ConfigIntegrationNames()))
	}
	if workflowName == "" {
		names := integration.ConfigWorkflowNames()
		if len(names) != 1 {
			return selection, fmt.Errorf("%s config requires a workflow: %s", integrationName, ValidNames(names))
		}
		selection.Workflow = names[0]
	}
	definition, known := integration.ConfigWorkflows[selection.Workflow]
	if !known {
		return selection, fmt.Errorf("unknown %s config workflow %q: %s", integrationName, selection.Workflow, ValidNames(integration.ConfigWorkflowNames()))
	}
	selection.Definition = definition
	return selection, nil
}

// ConfigOptionNames returns the union of delivered config option names for parsing.
func (c Catalog) ConfigOptionNames() []string {
	names := make(map[string]struct{})
	for _, integration := range c.Integrations {
		for _, workflow := range integration.ConfigWorkflows {
			for _, option := range workflow.Options {
				names[option] = struct{}{}
			}
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// ConfigUsage returns command forms from the delivered catalog, without a sandbox name.
func (c Catalog) ConfigUsage() []string {
	var forms []string
	for _, name := range c.ConfigIntegrationNames() {
		integration := c.Integrations[name]
		for _, workflow := range integration.ConfigWorkflowNames() {
			form := "integrations config " + name + " " + workflow
			for _, option := range integration.ConfigWorkflows[workflow].Options {
				form += " --" + option + " " + strings.ToUpper(option)
			}
			forms = append(forms, form)
		}
	}
	return forms
}

// ValidNames describes available choices, including an empty delivered catalog.
func ValidNames(names []string) string {
	if len(names) == 0 {
		return "no valid names are available yet"
	}
	return "valid names: " + strings.Join(names, ", ")
}
