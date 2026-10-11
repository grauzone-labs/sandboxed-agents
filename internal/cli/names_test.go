package cli_test

import (
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

func TestDerivedNamesIncludeControllerGroup(t *testing.T) {
	want := cli.ObjectNames{
		Container: "sandboxed-agents.team-2.Demo.one", Backup: "sandboxed-agents-backup.team-2.Demo.one",
		Workspace: "sandboxed-agents.team-2.Demo.one.workspace", Home: "sandboxed-agents.team-2.Demo.one.home",
		SSH: "sandboxed-agents.team-2.Demo.one.ssh",
	}
	if got := cli.DerivedNames("team-2", "Demo.one"); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}
