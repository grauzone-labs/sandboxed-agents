package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// TestSSHConfigStaysUndelivered proves the staged SSH definition is not selectable or advertised through the shared catalog's public interface.
func TestSSHConfigStaysUndelivered(t *testing.T) {
	integration, exists := catalog.Default.Integrations["ssh"]
	if !exists || len(integration.ConfigWorkflows) != 1 {
		t.Fatal("missing staged single SSH config definition")
	}
	if _, err := catalog.Default.ResolveConfig("ssh", ""); err == nil || !strings.Contains(err.Error(), `unknown integration "ssh"`) {
		t.Fatalf("resolve error=%v", err)
	}
	if slices.Contains(catalog.Default.ConfigIntegrationNames(), "ssh") {
		t.Fatal("advertised staged SSH integration")
	}
	if slices.Contains(catalog.Default.ConfigOptionNames(), "host") || slices.Contains(catalog.Default.ConfigOptionNames(), "user") {
		t.Fatal("advertised staged SSH options")
	}
	for _, form := range catalog.Default.ConfigUsage() {
		if strings.Contains(form, "config ssh") {
			t.Fatalf("advertised %q", form)
		}
	}
	if _, err := catalog.Default.ResolveLogin("ssh", ""); err == nil || !strings.Contains(err.Error(), `unknown integration "ssh"`) {
		t.Fatalf("login error=%v", err)
	}
}
