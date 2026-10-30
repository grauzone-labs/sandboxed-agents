package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const (
	// GitIntegration names the integration for global Git configuration.
	GitIntegration = "git"
	// GitHubIntegration names the integration for GitHub authentication.
	GitHubIntegration = "github"
	// AzureDevOpsIntegration names the integration for Azure DevOps authentication.
	AzureDevOpsIntegration = "azdo"
	// OrganizationOption names the Azure DevOps organization URL option.
	OrganizationOption = "organization"
	// GitIdentityWorkflow configures the global Git author name and email.
	GitIdentityWorkflow = "identity"
	// GitCredentialsWorkflow configures the sandbox's Git credential store.
	GitCredentialsWorkflow = "credentials"
	// GitCredentialStoreHelper stores Git credentials in the sandbox's home data.
	GitCredentialStoreHelper = "store --file=/home/agent/.git-credentials"
	// GitNameOption names the Git author name option.
	GitNameOption = "name"
	// GitEmailOption names the Git author email option.
	GitEmailOption = "email"
	// AzureIntegration names the integration for Azure authentication.
	AzureIntegration = "azure"
	// DeviceCodeWorkflow selects authentication using a code entered on the host.
	DeviceCodeWorkflow = "device-code"
	// BrowserWorkflow selects authentication using a browser callback.
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
	var names []string
	for name, workflow := range i.ConfigWorkflows {
		if workflow.Delivered {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// hasOnlyUnnamedConfigWorkflow reports whether the integration's single delivered config workflow has no name, as ssh's
// has, so its messages list no workflow names.
func (i Integration) hasOnlyUnnamedConfigWorkflow() bool {
	return slices.Equal(i.ConfigWorkflowNames(), []string{""})
}

// ConfigIntegrationNames returns integrations that have delivered config workflows.
func (c Catalog) ConfigIntegrationNames() []string {
	var names []string
	for name, integration := range c.Integrations {
		if len(integration.ConfigWorkflowNames()) > 0 {
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
	if !known || len(integration.ConfigWorkflowNames()) == 0 {
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
	if !known || !definition.Delivered {
		if integration.hasOnlyUnnamedConfigWorkflow() {
			return selection, fmt.Errorf("%s config takes no workflow name: %q", integrationName, selection.Workflow)
		}
		return selection, fmt.Errorf("unknown %s config workflow %q: %s", integrationName, selection.Workflow, ValidNames(integration.ConfigWorkflowNames()))
	}
	selection.Definition = definition
	return selection, nil
}

// ConfigOptionNames returns distinct delivered config options for parsing, documentation, and usage.
func (c Catalog) ConfigOptionNames() []string {
	names := make(map[string]struct{})
	for _, integration := range c.Integrations {
		for _, workflow := range integration.ConfigWorkflows {
			if !workflow.Delivered {
				continue
			}
			for _, option := range workflow.Options {
				names[option] = struct{}{}
			}
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// StagedConfigOptionNames returns distinct options of only the named integration's
// undelivered config workflows so parsing can reach ResolveConfig's delivery gate.
func (c Catalog) StagedConfigOptionNames(integrationName string) []string {
	names := make(map[string]struct{})
	for _, workflow := range c.Integrations[integrationName].ConfigWorkflows {
		if workflow.Delivered {
			continue
		}
		for _, option := range workflow.Options {
			names[option] = struct{}{}
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
			form := "integrations config " + name
			if workflow != "" {
				form += " " + workflow
			}
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
		if workflows := integration.ConfigWorkflowNames(); known && len(workflows) > 0 {
			if integration.hasOnlyUnnamedConfigWorkflow() {
				return "", fmt.Errorf("%s has no login workflow; use integrations config NAME %s", name, name)
			}
			return "", fmt.Errorf("%s has no login workflow; use integrations config NAME %s; config workflows: %s", name, name, ValidNames(workflows))
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
