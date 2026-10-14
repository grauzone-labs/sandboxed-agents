package cli_test

import (
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestManagerQueryBuilderRejectsInvalidRequests proves that unsafe targets, unknown operations, and invalid payloads yield no Podman arguments through the host manager argument builder.
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
	for _, request := range []struct {
		command string
		payload []string
	}{{"version", []string{"extra"}}, {"ssh-host-key", []string{"extra"}}, {"ssh-deauthorize", []string{"extra"}}, {"ssh-authorize", nil}, {"ssh-authorize", []string{"ssh-ed25519 AAAA"}}, {"ssh-authorize", []string{"one", "two"}}} {
		args, err := cli.ManagerQueryArgs("demo", "/manager", request.command, request.payload...)
		if err == nil || len(args) != 0 {
			t.Errorf("request=%+v arguments=%v error=%v", request, args, err)
		}
	}
}

// TestManagerQueriesExplicitlyRequestContainerRoot proves that administrative session and SSH requests name root and preserve validated arguments through the host manager argument builder.
func TestManagerQueriesExplicitlyRequestContainerRoot(t *testing.T) {
	for _, subcommand := range []string{"version", "sessions", "ssh-host-key", "ssh-deauthorize", "ssh-authorize"} {
		t.Run(subcommand, func(t *testing.T) {
			var payload []string
			if subcommand == "ssh-authorize" {
				payload = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
			}
			got, err := cli.ManagerQueryArgs("sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand, payload...)
			want := []string{"exec", "--user=0:0", "sandboxed-agents.team.demo", "/usr/local/bin/sandboxed-agents-manager", subcommand}
			want = append(want, payload...)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments=%q error=%v; want %q", got, err, want)
			}
		})
	}
}
