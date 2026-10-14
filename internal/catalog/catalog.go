// Package catalog defines catalog data shared by the host and manager.
package catalog

// Catalog contains the delivered agents, toolchains, and integrations.
type Catalog struct {
	Agents       map[string]Agent
	Toolchains   []string
	Integrations map[string]Integration
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
	Agents: map[string]Agent{
		"copilot":  {Delivered: true, Install: Install{Kind: NPM, Package: "@github/copilot"}, Command: "copilot", Documentation: "https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli"},
		"claude":   {Delivered: true, Install: Install{Kind: NPM, Package: "@anthropic-ai/claude-code"}, Command: "claude", Documentation: "https://code.claude.com/docs/en/overview"},
		"codex":    {Delivered: true, Install: Install{Kind: NPM, Package: "@openai/codex"}, Command: "codex", Documentation: "https://developers.openai.com/codex/cli"},
		"opencode": {Delivered: true, Install: Install{Kind: NPM, Package: "opencode-ai"}, Command: "opencode", Documentation: "https://opencode.ai/docs/"},
	},
	Toolchains: []string{"dotnet", "playwright", "azure", "native"},
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
