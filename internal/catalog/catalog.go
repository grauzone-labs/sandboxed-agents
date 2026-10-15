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
	LoginWorkflows  []string // Explicit positional names; empty means the delivered login accepts no workflow argument.
	ConfigWorkflows map[string]ConfigWorkflow
}

type LoginWorkflow struct {
	Delivered               bool
	Options                 map[string]string // option name to usage value label
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
		"copilot": {
			Delivered: true, Install: Install{Kind: NPM, Package: "@github/copilot"}, Command: "copilot",
			LoginWorkflows: map[string][]string{"device-code": {"login", "--device-code"}},
			LoginMessage:   "Open the URL shown by Copilot on your host and enter its device code.",
			Documentation:  "https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli",
		},
		"claude": {
			Delivered: true, Install: Install{Kind: NPM, Package: "@anthropic-ai/claude-code"}, Command: "claude",
			LoginWorkflows: map[string][]string{"browser": {"auth", "login"}},
			LoginMessage:   "Open the URL shown by Claude on your host and follow its authentication instructions.",
			Documentation:  "https://code.claude.com/docs/en/overview",
		},
		"codex": {
			Delivered: true, Install: Install{Kind: NPM, Package: "@openai/codex"}, Command: "codex",
			LoginWorkflows: map[string][]string{"browser": {"login"}, "device-code": {"login", "--device-auth"}},
			LoginMessage:   "Follow the Codex login instructions on your host. Browser login needs an SSH tunnel for the callback; device-code does not.",
			Documentation:  "https://developers.openai.com/codex/cli",
		},
		"opencode": {
			Delivered: true, Install: Install{Kind: NPM, Package: "opencode-ai"}, Command: "opencode",
			LoginWorkflows: map[string][]string{"provider": {"auth", "login"}},
			LoginMessage:   "Select your provider and follow the OpenCode login instructions.",
			Documentation:  "https://opencode.ai/docs/",
		},
	},
	Toolchains: []string{"dotnet", "playwright", "azure", "native"},
	Integrations: map[string]Integration{
		GitHubIntegration: {LoginWorkflows: []string{DeviceCodeWorkflow}, Login: &LoginWorkflow{
			Delivered:             true,
			InvalidOptionsMessage: "--organization is only valid for azdo",
		}},
		AzureIntegration: {LoginWorkflows: []string{DeviceCodeWorkflow, BrowserWorkflow}, Login: &LoginWorkflow{
			Delivered:             true,
			InvalidOptionsMessage: "--organization is only valid for azdo",
		}},
		AzureDevOpsIntegration: {LoginWorkflows: []string{}, Login: &LoginWorkflow{
			Delivered: true,
			Options:   map[string]string{OrganizationOption: "URL"}, RequiredOptions: []string{OrganizationOption},
			InvalidArgumentsMessage: "azdo requires --organization URL and no workflow",
		}},
		GitIntegration: {ConfigWorkflows: map[string]ConfigWorkflow{
			GitIdentityWorkflow: {
				Options:     []string{GitNameOption, GitEmailOption},
				GitSettings: []GitSetting{{Key: "user.name", Option: GitNameOption}, {Key: "user.email", Option: GitEmailOption}},
			},
			GitCredentialsWorkflow: {GitSettings: []GitSetting{{Key: "credential.helper", Value: GitCredentialStoreHelper}}},
		}},
	},
}
