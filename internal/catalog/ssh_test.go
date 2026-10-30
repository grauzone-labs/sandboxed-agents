package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// TestSSHConfigIsDelivered proves the SSH config workflow is selectable without a workflow name, refuses one, and is advertised with its --host and
// --user options and its command form, and that a login names the config command without an empty workflow list, through the shared catalog's public interface.
func TestSSHConfigIsDelivered(t *testing.T) {
	selection, err := catalog.Default.ResolveConfig("ssh", "")
	if err != nil || selection.Integration != "ssh" || !slices.Equal(selection.Definition.Options, []string{"host", "user"}) {
		t.Fatalf("selection=%+v error=%v", selection, err)
	}
	if _, err := catalog.Default.ResolveConfig("ssh", "extra"); err == nil || err.Error() != `ssh config takes no workflow name: "extra"` {
		t.Fatalf("workflow error=%v", err)
	}
	if !slices.Contains(catalog.Default.ConfigIntegrationNames(), "ssh") {
		t.Fatal("SSH integration not advertised")
	}
	if !slices.Contains(catalog.Default.ConfigOptionNames(), "host") || !slices.Contains(catalog.Default.ConfigOptionNames(), "user") {
		t.Fatal("SSH options not advertised")
	}
	if !slices.ContainsFunc(catalog.Default.ConfigUsage(), func(form string) bool { return strings.Contains(form, "config ssh") }) {
		t.Fatalf("SSH command form missing from %q", catalog.Default.ConfigUsage())
	}
	if _, err := catalog.Default.ResolveLogin("ssh", ""); err == nil || err.Error() != "ssh has no login workflow; use integrations config NAME ssh" {
		t.Fatalf("login error=%v", err)
	}
}
