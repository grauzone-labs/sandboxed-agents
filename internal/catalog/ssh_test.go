package catalog_test

import (
	"slices"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// TestSSHConfigIsDelivered proves the single SSH config definition resolves without a workflow name and advertises its integration, options, and usage form through the shared catalog's public interface, while SSH still has no login.
func TestSSHConfigIsDelivered(t *testing.T) {
	integration, exists := catalog.Default.Integrations["ssh"]
	if !exists || len(integration.ConfigWorkflows) != 1 {
		t.Fatal("missing single SSH config definition")
	}
	selection, err := catalog.Default.ResolveConfig("ssh", "")
	if err != nil || selection.Workflow != "" || !slices.Equal(selection.Definition.Options, []string{"host", "user"}) {
		t.Fatalf("selection=%+v error=%v", selection, err)
	}
	if !slices.Contains(catalog.Default.ConfigIntegrationNames(), "ssh") {
		t.Fatal("SSH integration not advertised")
	}
	if !slices.Contains(catalog.Default.ConfigOptionNames(), "host") || !slices.Contains(catalog.Default.ConfigOptionNames(), "user") {
		t.Fatal("SSH options not advertised")
	}
	if !slices.Contains(catalog.Default.ConfigUsage(), "integrations config ssh --host HOST --user USER") {
		t.Fatalf("usage=%q; want the SSH form", catalog.Default.ConfigUsage())
	}
	if len(catalog.Default.StagedConfigOptionNames("ssh")) != 0 {
		t.Fatal("delivered SSH options still reported as staged")
	}
	if _, err := catalog.Default.ResolveLogin("ssh", ""); err == nil || err.Error() != "ssh has no login workflow; use integrations config NAME ssh" {
		t.Fatalf("login error=%v", err)
	}
}
