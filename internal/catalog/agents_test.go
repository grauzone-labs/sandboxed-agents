package catalog_test

import (
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
)

// TestDeliveredAgentsShareInstallData proves delivered names and npm package commands come from one catalog while future entries remain unknown at the catalog public seam.
func TestDeliveredAgentsShareInstallData(t *testing.T) {
	if got := catalog.Default.AgentNames(); !reflect.DeepEqual(got, []string{"claude", "codex", "copilot", "opencode"}) {
		t.Fatalf("delivered names=%v", got)
	}
	for _, tc := range []struct{ name, pkg, command string }{
		{"copilot", "@github/copilot", "copilot"},
		{"claude", "@anthropic-ai/claude-code", "claude"},
		{"codex", "@openai/codex", "codex"},
		{"opencode", "opencode-ai", "opencode"},
	} {
		got, err := catalog.Default.ResolveAgent(tc.name)
		if err != nil || got.Install.Kind != catalog.NPM || got.Install.Package != tc.pkg || got.Command != tc.command || got.Documentation == "" {
			t.Fatalf("%s=%+v error=%v", tc.name, got, err)
		}
	}
	c := catalog.Catalog{Agents: map[string]catalog.Agent{"future": {Install: catalog.Install{Kind: catalog.NPM, Package: "future"}, Command: "future"}, "new": {Delivered: true, Install: catalog.Install{Kind: catalog.NPM, Package: "new"}, Command: "new"}}}
	if got := c.AgentNames(); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("names=%v", got)
	}
	if _, err := c.ResolveAgent("future"); err == nil {
		t.Fatal("undelivered agent resolved")
	}
}

// TestPackageVersionsRequireExactSemanticVersions proves literal release, prerelease, and build versions are accepted while ranges, tags, injection, and invalid numeric forms are rejected at catalog.ValidateVersion.
func TestPackageVersionsRequireExactSemanticVersions(t *testing.T) {
	for _, version := range []string{"0.0.0", "1.2.3", "10.20.30-rc.1", "1.2.3+build.01", "1.2.3-alpha-beta.0+build.3"} {
		if err := catalog.ValidateVersion(version); err != nil {
			t.Fatalf("exact version %q refused: %v", version, err)
		}
	}
	for _, version := range []string{"", "latest", "beta", "*", "^1.2.3", "~1.2.3", "1.2.x", ">=1.2.3", "1.2.3 || 2.0.0", "file:/tmp/pkg", "01.2.3", "1.02.3", "1.2.03", "1.2.3-01", "1.2.3-rc.01", "1.2.3;id", "1.2.3\n"} {
		if err := catalog.ValidateVersion(version); err == nil {
			t.Fatalf("unsafe/non-exact version %q accepted", version)
		}
	}
}

// TestPinVersionsRejectBuildMetadata proves pin versions reject npm-normalized build metadata while installed package metadata remains valid at the catalog validation seams.
func TestPinVersionsRejectBuildMetadata(t *testing.T) {
	for _, version := range []string{"1.2.3+build.4", "1.2.3-rc.1+build.4"} {
		if err := catalog.ValidateVersion(version); err != nil {
			t.Fatalf("installed version %q refused: %v", version, err)
		}
		if err := catalog.ValidatePinVersion(version); err == nil {
			t.Fatalf("build metadata pin %q accepted", version)
		}
	}
	for _, version := range []string{"1.2.3", "1.2.3-rc.1"} {
		if err := catalog.ValidatePinVersion(version); err != nil {
			t.Fatalf("exact pin %q refused: %v", version, err)
		}
	}
}
