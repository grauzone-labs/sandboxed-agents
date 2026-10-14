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
	Options     []string
	GitSettings []GitSetting
}

var Default = Catalog{
	Agents:              []string{},
	AgentLoginWorkflows: map[string][]string{},
	Toolchains:          []string{},
	Integrations: map[string]Integration{
		"github": {LoginWorkflows: []string{}, Login: &LoginWorkflow{
			WorkflowOmittable: true, InvalidOptionsMessage: "--organization is only valid for azdo",
		}},
		"azure": {LoginWorkflows: []string{}, Login: &LoginWorkflow{
			WorkflowOmittable: true, InvalidOptionsMessage: "--organization is only valid for azdo",
		}},
		"azdo": {LoginWorkflows: []string{}, Login: &LoginWorkflow{
			Options: []string{"organization"}, RequiredOptions: []string{"organization"},
			InvalidArgumentsMessage: "azdo requires --organization URL and no workflow",
		}},
		GitIntegration: {ConfigWorkflows: map[string]ConfigWorkflow{
			GitIdentityWorkflow: {
				Options:     []string{GitNameOption, GitEmailOption},
				GitSettings: []GitSetting{{Key: "user.name", Option: GitNameOption}, {Key: "user.email", Option: GitEmailOption}},
			},
			GitCredentialsWorkflow: {GitSettings: []GitSetting{{Key: "credential.helper", Value: "store --file=/home/agent/.git-credentials"}}},
		}},
	},
}
