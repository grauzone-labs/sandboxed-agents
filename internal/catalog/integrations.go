package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	GitIntegration           = "git"
	GitHubIntegration        = "github"
	AzureDevOpsIntegration   = "azdo"
	OrganizationOption       = "organization"
	GitIdentityWorkflow      = "identity"
	GitCredentialsWorkflow   = "credentials"
	GitCredentialStoreHelper = "store --file=/home/agent/.git-credentials"
	GitNameOption            = "name"
	GitEmailOption           = "email"
	AzureIntegration = "azure"
	DeviceCodeWorkflow = "device-code"
	BrowserWorkflow = "browser"
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

// LoginIntegrationNames lists only integrations whose login handler is delivered.
func (c Catalog) LoginIntegrationNames() []string {
	var names []string
	for name, integration := range c.Integrations {
		if integration.Login != nil && integration.Login.Delivered {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// ResolveLogin validates delivered names and resolves an omitted single workflow.
func (c Catalog) ResolveLogin(name, workflow string) (string, error) {
	integration, known := c.Integrations[name]
	if !known || integration.Login == nil || !integration.Login.Delivered {
		if known && len(integration.ConfigWorkflows) > 0 {
			return "", fmt.Errorf("%s has no login workflow; use integrations config NAME %s; config workflows: %s", name, name, ValidNames(integration.ConfigWorkflowNames()))
		}
		return "", fmt.Errorf("unknown integration %q: %s", name, ValidNames(c.LoginIntegrationNames()))
	}
	if len(integration.LoginWorkflows) == 0 {
		if workflow != "" {
			return "", fmt.Errorf("%s", integration.Login.InvalidArgumentsMessage)
		}
		return "", nil
	}
	if workflow == "" {
		if len(integration.LoginWorkflows) == 1 {
			return integration.LoginWorkflows[0], nil
		}
		return "", fmt.Errorf("%s login requires a workflow: %s", name, ValidNames(integration.LoginWorkflows))
	}
	if !slices.Contains(integration.LoginWorkflows, workflow) {
		return "", fmt.Errorf("unknown workflow %q: %s", workflow, ValidNames(integration.LoginWorkflows))
	}
	return workflow, nil
}

// LoginUsage returns the delivered manager login command forms.
func (c Catalog) LoginUsage() []string {
	var forms []string
	for _, name := range c.LoginIntegrationNames() {
		integration := c.Integrations[name]
		workflows := integration.LoginWorkflows
		if len(workflows) == 0 {
			workflows = []string{""}
		}
		for _, workflow := range workflows {
			form := "integrations login " + name
			if workflow != "" {
				form += " " + workflow
			}
			for _, option := range slices.Sorted(maps.Keys(integration.Login.Options)) {
				form += " --" + option + " " + integration.Login.Options[option]
			}
			forms = append(forms, form)
		}
	}
	return forms
}
