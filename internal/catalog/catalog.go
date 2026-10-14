// Package catalog defines catalog data shared by the host and manager.
package catalog

// Catalog contains the delivered agents, toolchains, and integrations.
type Catalog struct {
	Agents              []string
	AgentLoginWorkflows map[string][]string
	Toolchains          []string
	Integrations        map[string]Integration
}

type Integration struct {
	Login           *LoginWorkflow
	LoginWorkflows  []string
	ConfigWorkflows map[string]ConfigWorkflow
}

type LoginWorkflow struct {
	WorkflowOmittable       bool
	Options                 []string
	RequiredOptions         []string
	InvalidOptionsMessage   string
	InvalidArgumentsMessage string
}

type ConfigWorkflow struct {
	Options []string
}

var Default = Catalog{
	Agents:              []string{},
	AgentLoginWorkflows: map[string][]string{},
	Toolchains:          []string{},
	Integrations: map[string]Integration{
		"git": {ConfigWorkflows: map[string]ConfigWorkflow{
			"identity":    {Options: []string{"name", "email"}},
			"credentials": {},
		}},
	},
}
