package cli_test

import (
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestManagerQueryBuilderRejectsInvalidRequests proves that unsafe targets and non-query commands yield no Podman arguments through the host manager argument builder.
func TestManagerQueryBuilderRejectsInvalidRequests(t *testing.T) {
	for _, request := range [][3]string{
		{"", "/manager", "version"}, {"--privileged", "/manager", "version"},
		{"demo\nother", "/manager", "version"}, {"demo\x00other", "/manager", "version"},
		{"demo", "", "version"}, {"demo", "relative-manager", "version"},
		{"demo", "/manager\x00other", "version"}, {"demo", "/manager", "work"},
	} {
		args, err := cli.ManagerQueryArgs(request[0], request[1], request[2])
		if err == nil || len(args) != 0 {
			t.Errorf("request=%q arguments=%q error=%v; want rejection without arguments", request, args, err)
		}
	}
}

// TestManagerQueriesExplicitlyRequestContainerRoot proves that version and session queries name root and preserve arguments through the host manager argument builder.
func TestManagerQueriesExplicitlyRequestContainerRoot(t *testing.T) {
	for _, subcommand := range []string{"version", "sessions"} {
		t.Run(subcommand, func(t *testing.T) {
			got, err := cli.ManagerQueryArgs("sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand)
			want := []string{"exec", "--user=0:0", "sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments=%q error=%v; want %q", got, err, want)
			}
		})
	}
}
