package cli_test

import (
	"reflect"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

func TestParsedUpKeepsDefaultsAndEmptyToolchainSet(t *testing.T) {
	request, err := cli.Parse([]string{"up", "demo", "--with", "none"}, "default")
	want := cli.Resources{Memory: 8589934592, CPUs: 4, PIDsLimit: 2048, ShmSize: 1073741824}
	if err != nil || request.Resources != want || len(request.Toolchains) != 0 || request.Options["with"] != "none" {
		t.Fatalf("request=%+v error=%v; want resources=%+v and an explicit empty toolchain set", request, err, want)
	}
}

func TestParsedRunPreservesEveryProgramArgument(t *testing.T) {
	agents := catalog.Default.Agents
	catalog.Default.Agents = []string{"codex"}
	t.Cleanup(func() { catalog.Default.Agents = agents })
	request, err := cli.Parse([]string{"agents", "run", "demo", "codex", "--version", "two words", "", "--"}, "team")
	want := []string{"demo", "codex", "--version", "two words", "", "--"}
	if err != nil || request.Name != "demo" || request.Group != "team" || !reflect.DeepEqual(request.Args, want) {
		t.Fatalf("request=%+v error=%v", request, err)
	}
}

func TestAgentLoginRejectsUnknownWorkflowForKnownAgent(t *testing.T) {
	agents := catalog.Default.Agents
	catalog.Default.Agents = []string{"codex"}
	t.Cleanup(func() { catalog.Default.Agents = agents })
	_, err := cli.Parse([]string{"agents", "login", "demo", "codex", "bogusflow"}, "default")
	want := `unknown workflow "bogusflow": no valid names are available yet`
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v; want %q", err, want)
	}
}
