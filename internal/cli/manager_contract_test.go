package cli_test

import (
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestManagerQueryLiteralPayloads proves agent and Tool reads and key canonicalization preserve literal argv at ManagerQueryArgs.
func TestManagerQueryLiteralPayloads(t *testing.T) {
	for _, tc := range []struct {
		name          string
		payload, want []string
	}{
		{"agents", []string{"list"}, []string{"exec", "--user=0:0", "demo", "/manager", "agents", "list"}},
		{"agents", []string{"status", "opencode"}, []string{"exec", "--user=0:0", "demo", "/manager", "agents", "status", "opencode"}},
		{"tools", []string{"list"}, []string{"exec", "--user=0:0", "demo", "/manager", "tools", "list"}},
		{"ssh-authorize", []string{"  ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA comment  "}, []string{"exec", "--user=0:0", "demo", "/manager", "ssh-authorize", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
	} {
		got, err := cli.ManagerQueryArgs("demo", "/manager", tc.name, tc.payload...)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s %q: got %q, error %v; want %q", tc.name, tc.payload, got, err, tc.want)
		}
	}
}
