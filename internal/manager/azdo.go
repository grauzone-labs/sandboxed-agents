package manager

import (
	"fmt"
	"io"
	"strings"

	"github.com/grauzone-labs/sandboxed-agents/internal/azdo"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// loginAzureDevOps stores the PAT through stdin-only CLI interfaces after verifying agent identity.
func loginAzureDevOps(args []string, stderr io.Writer, effects Effects) int {
	if _, err := catalog.Default.ResolveLogin(catalog.AzureDevOpsIntegration, ""); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) != 2 || args[0] != "--"+catalog.OrganizationOption {
		fmt.Fprintln(stderr, catalog.Default.Integrations[catalog.AzureDevOpsIntegration].Login.InvalidArgumentsMessage)
		return 2
	}
	organization, username, err := catalog.AzureDevOpsOrganization(args[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !verifyAgent(effects, stderr) {
		return 1
	}
	store, err := credentialStore(effects)
	if err != nil {
		fmt.Fprintln(stderr, "Git credential store is unavailable; run integrations config NAME git credentials")
		return 1
	}
	if err := store.Check(); err != nil {
		fmt.Fprintln(stderr, "Git credential store is missing or locked; run integrations config NAME git credentials")
		return 1
	}
	token, err := azdo.ReadToken(effects.Stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	p := Process{Program: "az", Args: []string{"devops", "login", "--" + catalog.OrganizationOption, organization, "--only-show-errors"}, Stdin: strings.NewReader(token + "\n"), Identity: agentIdentity, EnvironmentPolicy: AzureDevOpsEnvironment, Stdout: io.Discard, Stderr: io.Discard}
	if err := effects.Start(p); err != nil {
		fmt.Fprintln(stderr, "Azure DevOps login failed while running az; credentials may be partially configured")
		return 1
	}
	if err := store.StoreAzureDevOps(organization, username, token); err != nil {
		fmt.Fprintln(stderr, "Azure DevOps login failed while storing Git credentials; credentials may be partially configured")
		return 1
	}
	return 0
}
