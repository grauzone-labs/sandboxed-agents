package cli_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/user"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
)

// helpGroups lists each command group with its subcommands, as issue #132 names them.
var helpGroups = map[string][]string{
	"agents":       {"enable", "disable", "update", "login", "status", "run", "session"},
	"integrations": {"login", "config"},
	"tools":        {"enable", "update", "status", "connect", "tailscale", "pair", "delete"},
}

// helpTopLevelLeaves lists the leaf commands outside the groups, as issue #132 names them.
var helpTopLevelLeaves = []string{"version", "list", "check", "build", "up", "start", "stop", "restart", "remove", "update", "shell", "ssh-config", "fingerprint"}

// helpLeafOptions lists each leaf command's options as docs/command-reference.md#grammar spells them.
var helpLeafOptions = map[string][]string{
	"build":          {"--with SET"},
	"up":             {"--with SET", "--agents LIST", "--port N", "--ssh-config", "--memory SIZE", "--cpus N", "--pids-limit N", "--shm-size SIZE"},
	"start":          {"--ssh-config"},
	"stop":           {"--force"},
	"restart":        {"--force"},
	"remove":         {"--volumes", "--force"},
	"update":         {"--with SET", "--force", "--all"},
	"ssh-config":     {"--install", "--remove"},
	"agents enable":  {"--version X", "--force"},
	"agents disable": {"--force"},
	"agents update":  {"--unpin", "--force"},
	"agents session": {"--stop"},
	"tools enable":   {"--channel CHANNEL", "--version X"},
	"tools update":   {"--channel CHANNEL", "--version X"},
	"tools pair":     {"--ttl DURATION", "--revoke"},
}

// helpLeaves returns every leaf command path, top-level leaves first.
func helpLeaves() []string {
	leaves := append([]string(nil), helpTopLevelLeaves...)
	for _, group := range []string{"agents", "integrations", "tools"} {
		for _, subcommand := range helpGroups[group] {
			leaves = append(leaves, group+" "+subcommand)
		}
	}
	return leaves
}

// helpHost is a host whose every host query fails the test, so a help path that reads host
// facts, files, or users is caught at the RunWithHost seam.
func helpHost(t *testing.T) cli.Host {
	t.Helper()
	return cli.Host{
		Platform: "linux", Architecture: "amd64",
		ReadFile: func(path string) ([]byte, error) {
			t.Errorf("help read host file %s", path)
			return nil, errors.New("help must not read host files")
		},
		CurrentUser: func() (*user.User, error) {
			t.Error("help queried the current user")
			return nil, errors.New("help must not query the user")
		},
		PortAvailable: func(uint16) error {
			t.Error("help probed a loopback port")
			return nil
		},
		WorkspacePath: func(path string) (string, error) {
			t.Errorf("help resolved workspace %s", path)
			return "", errors.New("help must not resolve a workspace")
		},
		Stdin:            strings.NewReader(""),
		IsInputTerminal:  func(io.Reader) bool { return false },
		IsOutputTerminal: func(io.Writer) bool { return false },
	}
}

// runHelp runs the public CLI with a host that refuses every query and a fake Podman on PATH, and
// returns the status, both streams, and the recorded Podman calls.
func runHelp(t *testing.T, args ...string) (int, string, string, [][]string) {
	t.Helper()
	f := linuxHost(t)
	var stdout, stderr bytes.Buffer
	status := cli.RunWithHost(args, &stdout, &stderr, helpHost(t))
	return status, stdout.String(), stderr.String(), testutil.Invocations(t, f.fakes.PodmanLog)
}

// listsEntry reports whether text lists name in an indented line followed by a description.
func listsEntry(text, name string) bool {
	return regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + ` +\S`).MatchString(text)
}

// TestRootHelpListsEveryCommandOnStdout proves --help, -h, and help print the same root help with
// a usage line and every command and group with a one-line description to stdout, exit 0, and
// write nothing to stderr, at the cli.RunWithHost seam.
func TestRootHelpListsEveryCommandOnStdout(t *testing.T) {
	var first string
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		status, stdout, stderr, _ := runHelp(t, args...)
		if status != 0 || stderr != "" || !strings.Contains(stdout, "usage: sandboxed-agents COMMAND") {
			t.Fatalf("%v: status=%d stdout=%q stderr=%q", args, status, stdout, stderr)
		}
		for _, name := range append(append([]string(nil), helpTopLevelLeaves...), "agents", "integrations", "tools") {
			if !listsEntry(stdout, name) {
				t.Errorf("%v: root help omits %s with a description:\n%s", args, name, stdout)
			}
		}
		if first == "" {
			first = stdout
		} else if stdout != first {
			t.Errorf("%v printed different root help:\n%s\nwant:\n%s", args, stdout, first)
		}
	}
}

// TestBareInvocationPrintsRootUsageOnStderr proves a run without arguments keeps its message,
// prints the root usage and command list to stderr with a pointer to --help, and exits 2, at the
// cli.RunWithHost seam.
func TestBareInvocationPrintsRootUsageOnStderr(t *testing.T) {
	status, stdout, stderr, calls := runHelp(t)
	if status != 2 || stdout != "" || len(calls) != 0 {
		t.Fatalf("status=%d stdout=%q calls=%v", status, stdout, calls)
	}
	if !strings.HasPrefix(stderr, "step 1: a command is required\nusage: sandboxed-agents COMMAND") || !listsEntry(stderr, "up") || !listsEntry(stderr, "tools") {
		t.Fatalf("stderr=%q; want the message, then the root usage and command list", stderr)
	}
	if !strings.HasSuffix(stderr, "run 'sandboxed-agents --help' for usage\n") {
		t.Fatalf("stderr=%q; want the root help pointer last", stderr)
	}
}

// TestGroupHelpListsSubcommands proves GROUP --help, GROUP -h, GROUP help, and help GROUP print the
// group's usage and each subcommand with a one-line description to stdout and exit 0, while the
// bare group prints the same list to stderr after its message and exits 2, at the cli.RunWithHost
// seam.
func TestGroupHelpListsSubcommands(t *testing.T) {
	for group, subcommands := range helpGroups {
		var first string
		for _, args := range [][]string{{group, "--help"}, {group, "-h"}, {group, "help"}, {"help", group}} {
			status, stdout, stderr, _ := runHelp(t, args...)
			if status != 0 || stderr != "" || !strings.Contains(stdout, "usage: sandboxed-agents "+group+" SUBCOMMAND") {
				t.Fatalf("%v: status=%d stdout=%q stderr=%q", args, status, stdout, stderr)
			}
			for _, subcommand := range subcommands {
				if !listsEntry(stdout, subcommand) {
					t.Errorf("%v: group help omits %s:\n%s", args, subcommand, stdout)
				}
			}
			if first == "" {
				first = stdout
			} else if stdout != first {
				t.Errorf("%v printed different group help:\n%s\nwant:\n%s", args, stdout, first)
			}
		}
		status, stdout, stderr, _ := runHelp(t, group)
		if status != 2 || stdout != "" || !strings.HasPrefix(stderr, "step 1: "+group+" requires a subcommand\nusage: sandboxed-agents "+group+" SUBCOMMAND") {
			t.Fatalf("bare %s: status=%d stdout=%q stderr=%q", group, status, stdout, stderr)
		}
		for _, subcommand := range subcommands {
			if !listsEntry(stderr, subcommand) {
				t.Errorf("bare %s omits %s:\n%s", group, subcommand, stderr)
			}
		}
		if !strings.HasSuffix(stderr, "run 'sandboxed-agents "+group+" --help' for usage\n") {
			t.Errorf("bare %s: stderr=%q; want the group help pointer", group, stderr)
		}
	}
}

// TestLeafHelpShowsUsageOptionsAndExample proves CMD --help, CMD -h, CMD NAME --help, and help CMD
// print one leaf help with its description, usage, every option of the documented grammar, and an
// example to stdout and exit 0 for every leaf command, at the cli.RunWithHost seam.
func TestLeafHelpShowsUsageOptionsAndExample(t *testing.T) {
	for _, leaf := range helpLeaves() {
		path := strings.Fields(leaf)
		var first string
		for _, args := range [][]string{
			append(append([]string(nil), path...), "--help"),
			append(append([]string(nil), path...), "-h"),
			append(append([]string(nil), path...), "agent01", "--help"),
			append([]string{"help"}, path...),
		} {
			status, stdout, stderr, _ := runHelp(t, args...)
			if status != 0 || stderr != "" {
				t.Fatalf("%v: status=%d stdout=%q stderr=%q", args, status, stdout, stderr)
			}
			lines := strings.Split(stdout, "\n")
			if strings.TrimSpace(lines[0]) == "" || strings.HasPrefix(lines[0], "usage:") {
				t.Errorf("%v: help does not open with a description:\n%s", args, stdout)
			}
			if !strings.Contains(stdout, "usage: sandboxed-agents "+leaf) {
				t.Errorf("%v: help lacks the usage line:\n%s", args, stdout)
			}
			if !regexp.MustCompile(`(?m)^Examples?:\n  sandboxed-agents ` + regexp.QuoteMeta(leaf)).MatchString(stdout) {
				t.Errorf("%v: help lacks an example:\n%s", args, stdout)
			}
			for _, option := range helpLeafOptions[leaf] {
				if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(option) + `( |$)`).MatchString(stdout) {
					t.Errorf("%v: help does not list option %s:\n%s", args, option, stdout)
				}
			}
			if first == "" {
				first = stdout
			} else if stdout != first {
				t.Errorf("%v printed different help:\n%s\nwant:\n%s", args, stdout, first)
			}
		}
	}
}

// TestLeafHelpShowsDefaultsAndBothUpdateForms proves up's help names the default resource limits and
// update's help shows both the NAME and --all forms, at the cli.RunWithHost seam.
func TestLeafHelpShowsDefaultsAndBothUpdateForms(t *testing.T) {
	_, stdout, _, _ := runHelp(t, "up", "--help")
	for _, option := range []string{"--memory SIZE", "--cpus N", "--pids-limit N", "--shm-size SIZE", "--port N"} {
		want := map[string]string{"--memory SIZE": "8g", "--cpus N": "4", "--pids-limit N": "2048", "--shm-size SIZE": "1g", "--port N": "2222"}[option]
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(option) + ` .*\(default ` + regexp.QuoteMeta(want) + ``).MatchString(stdout) {
			t.Errorf("up help does not give %s the default %s:\n%s", option, want, stdout)
		}
	}
	_, stdout, _, _ = runHelp(t, "update", "--help")
	for _, form := range []string{"sandboxed-agents update NAME [--with SET] [--force]", "sandboxed-agents update --all [--force]"} {
		if !strings.Contains(stdout, form) {
			t.Errorf("update help lacks %q:\n%s", form, stdout)
		}
	}
}

// TestLeafHelpListsCatalogValues proves the agents, integrations, and tools leaf help lists the
// agents, integrations with their workflows and options, toolchains, Tools, channels, and actions
// that the catalog holds, including entries added to the catalog, at the cli.RunWithHost seam.
func TestLeafHelpListsCatalogValues(t *testing.T) {
	integrations := catalog.Default.Integrations
	catalog.Default.Integrations = map[string]catalog.Integration{}
	for name, integration := range integrations {
		catalog.Default.Integrations[name] = integration
	}
	catalog.Default.Integrations["vault"] = catalog.Integration{
		LoginWorkflows: []string{"token"},
		Login:          &catalog.LoginWorkflow{Delivered: true, Options: map[string]string{"address": "ADDR"}},
		ConfigWorkflows: map[string]catalog.ConfigWorkflow{
			"policy": {Delivered: true, Options: []string{"path"}},
		},
	}
	tools := catalog.Default.Tools
	catalog.Default.Tools = map[string]catalog.Tool{}
	for name, tool := range tools {
		catalog.Default.Tools[name] = tool
	}
	catalog.Default.Tools["lighthouse"] = catalog.Tool{Delivered: true, Channels: []string{"stable"}, ConnectActions: map[string][]string{"attach": nil}}
	toolchains := catalog.Default.Toolchains
	catalog.Default.Toolchains = append(append([]string(nil), toolchains...), "rust")
	t.Cleanup(func() {
		catalog.Default.Integrations, catalog.Default.Tools, catalog.Default.Toolchains = integrations, tools, toolchains
	})
	for _, tc := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"agents", "enable", "--help"}, []string{"claude", "codex", "copilot", "opencode"}},
		{[]string{"agents", "login", "--help"}, []string{"codex", "browser", "device-code", "opencode", "provider"}},
		{[]string{"up", "--help"}, []string{"dotnet", "playwright", "azure", "native", "rust", "claude"}},
		{[]string{"integrations", "login", "--help"}, []string{"github", "azure", "device-code", "browser", "azdo", "--organization URL", "vault", "token", "--address ADDR"}},
		{[]string{"integrations", "config", "--help"}, []string{"git", "identity", "credentials", "--name", "--email", "ssh", "--host", "--user", "vault", "policy", "--path"}},
		{[]string{"tools", "enable", "--help"}, []string{"t3code", "lighthouse", "latest", "nightly", "stable"}},
		{[]string{"tools", "connect", "--help"}, []string{"t3code", "login", "link", "unlink", "logout", "lighthouse", "attach"}},
		{[]string{"tools", "tailscale", "--help"}, []string{"t3code", "lighthouse", "login", "logout"}},
		{[]string{"tools", "delete", "--help"}, []string{"t3code", "lighthouse"}},
	} {
		status, stdout, _, _ := runHelp(t, tc.args...)
		for _, want := range tc.wants {
			if status != 0 || !regexp.MustCompile(`(^|[\s,(:])`+regexp.QuoteMeta(want)+`($|[\s,.;)])`).MatchString(stdout) {
				t.Errorf("%v: status=%d; help lacks catalog value %q:\n%s", tc.args, status, want, stdout)
			}
		}
	}
}

// TestUsageErrorsNameTheDeepestHelp proves an unknown command, subcommand, or option and a missing
// name or argument keep their message and exit status 2 and are followed by the usage and help
// pointer of the deepest recognised level, at the cli.RunWithHost seam.
func TestUsageErrorsNameTheDeepestHelp(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		message, usage  string
		pointer         string
		forbiddenOutput string
	}{
		{[]string{"bogus"}, `unknown command "bogus"`, "usage: sandboxed-agents COMMAND", "sandboxed-agents --help", ""},
		{[]string{"help", "bogus"}, `unknown command "bogus"`, "usage: sandboxed-agents COMMAND", "sandboxed-agents --help", ""},
		{[]string{"tools", "bogus"}, `unknown command "tools bogus"`, "usage: sandboxed-agents tools SUBCOMMAND", "sandboxed-agents tools --help", ""},
		{[]string{"help", "tools", "bogus"}, `unknown command "tools bogus"`, "usage: sandboxed-agents tools SUBCOMMAND", "sandboxed-agents tools --help", ""},
		{[]string{"up"}, "sandbox name must follow the command path", "usage: sandboxed-agents up NAME", "sandboxed-agents up --help", "Examples:"},
		{[]string{"up", "agent01", "--bogus"}, `unknown option "--bogus"`, "usage: sandboxed-agents up NAME", "sandboxed-agents up --help", "Examples:"},
		{[]string{"tools", "enable", "agent01"}, "wrong number of arguments for tools enable", "usage: sandboxed-agents tools enable NAME TOOL", "sandboxed-agents tools enable --help", "Examples:"},
		{[]string{"tools", "enable", "--channel", "latest"}, "sandbox name must follow the command path", "usage: sandboxed-agents tools enable NAME TOOL", "sandboxed-agents tools enable --help", "Examples:"},
		{[]string{"agents", "run", "agent01"}, "agents run requires NAME AGENT", "usage: sandboxed-agents agents run NAME AGENT", "sandboxed-agents agents run --help", "Examples:"},
		{[]string{"update"}, "sandbox name must follow the command path", "usage: sandboxed-agents update NAME [--with SET] [--force]\n       sandboxed-agents update --all [--force]", "sandboxed-agents update --help", "Examples:"},
	} {
		status, stdout, stderr, calls := runHelp(t, tc.args...)
		if status != 2 || stdout != "" || len(calls) != 0 {
			t.Fatalf("%v: status=%d stdout=%q calls=%v", tc.args, status, stdout, calls)
		}
		if !strings.HasPrefix(stderr, "step 1: "+tc.message+"\n"+tc.usage) {
			t.Errorf("%v: stderr=%q; want message %q then usage %q", tc.args, stderr, tc.message, tc.usage)
		}
		if !strings.HasSuffix(stderr, "run '"+tc.pointer+"' for usage\n") {
			t.Errorf("%v: stderr=%q; want pointer to %s", tc.args, stderr, tc.pointer)
		}
		if tc.forbiddenOutput != "" && strings.Contains(stderr, tc.forbiddenOutput) {
			t.Errorf("%v: a leaf usage error printed the full help: %q", tc.args, stderr)
		}
	}
}

// TestHelpMakesNoPodmanCallAndWritesNoHostState proves every help form at every level of the
// command chain makes no Podman call, queries no host fact, and leaves the host state roots empty,
// at the cli.RunWithHost seam with a fake Podman on PATH.
func TestHelpMakesNoPodmanCallAndWritesNoHostState(t *testing.T) {
	forms := [][]string{{"--help"}, {"-h"}, {"help"}}
	for group := range helpGroups {
		forms = append(forms, []string{group, "--help"}, []string{group, "-h"}, []string{group, "help"}, []string{"help", group})
	}
	for _, leaf := range helpLeaves() {
		path := strings.Fields(leaf)
		forms = append(forms,
			append(append([]string(nil), path...), "--help"),
			append(append([]string(nil), path...), "agent01", "--help"),
			append([]string{"help"}, path...))
	}
	for _, args := range forms {
		status, stdout, _, calls := runHelp(t, args...)
		if status != 0 || stdout == "" || len(calls) != 0 {
			t.Fatalf("%v: status=%d calls=%v", args, status, calls)
		}
		for _, variable := range []string{"XDG_STATE_HOME", "LOCALAPPDATA"} {
			entries, err := os.ReadDir(os.Getenv(variable))
			if err != nil || len(entries) != 0 {
				t.Fatalf("%v: %s holds %v (%v); help must write no host state", args, variable, entries, err)
			}
		}
	}
}

// TestHelpReservesNoSandboxName proves up help still creates a sandbox named help, as ADR-0004
// requires, through RunWithHost in the up executable boundary against fake Podman.
func TestHelpReservesNoSandboxName(t *testing.T) {
	f := linuxHost(t)
	scriptNewSandbox(t, f, "default", "help")
	status, output, calls := runUpExecutable(t, f, "up", "help")
	if status != 0 || !strings.Contains(output, "sandbox help running") || strings.Contains(output, "usage:") {
		t.Fatalf("status=%d output=%q calls=%v", status, output, calls)
	}
	assertNewSandbox(t, calls, "", "default", "help", baseTag(t), "", "2222", "8589934592", "4", "2048", "1073741824")
}

// TestAgentRunPassesHelpToTheAgent proves agents run NAME AGENT --help -h passes both help flags to
// the agent unchanged instead of printing help, through RunWithHost and fake Podman.
func TestAgentRunPassesHelpToTheAgent(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.IsInputTerminal = func(io.Reader) bool { return false }
	f.host.IsOutputTerminal = func(io.Writer) bool { return false }
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"claude"] = response{Output: `{"name":"claude","enabled":true,"installed_version":"1.2.3"}`}
	execution := strings.Fields("exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents run claude --help -h")
	f.responses[strings.Join(execution, " ")] = response{Output: "agent help\n", Exit: 3}
	status, output, calls := f.run(t, "agents", "run", "agent01", "claude", "--help", "-h")
	want := append(sandboxQueryCalls(), strings.Fields(agentWorkProbe), strings.Fields(agentWorkStatus+"claude"), execution)
	if status != 3 || output != "agent help\n" || !reflect.DeepEqual(calls, want) {
		t.Fatalf("status=%d output=%q calls=%v want=%v", status, output, calls, want)
	}
}
