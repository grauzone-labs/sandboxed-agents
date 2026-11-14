package cli_test

import (
	"bytes"
	"github.com/grauzone-labs/sandboxed-agents/internal/catalog"
	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
	"github.com/grauzone-labs/sandboxed-agents/internal/testutil"
	"os"
	"strings"
	"testing"
)

// TestAgentLoginWorkflowHelpShowsOnlyItsOptions proves selected workflow help describes OpenCode's requirements, removal conflicts, and examples without execution through RunWithHost.
func TestAgentLoginWorkflowHelpShowsOnlyItsOptions(t *testing.T) {
	status, stdout, stderr, calls := runHelp(t, "agents", "login", "not/a/sandbox", "opencode", "proxy", "--help")
	if status != 0 || stderr != "" || len(calls) != 0 {
		t.Fatalf("status=%d stderr=%q calls=%v", status, stderr, calls)
	}
	for _, want := range []string{"agents login NAME opencode proxy", "--url URL", "--model ID", "Required unless --remove", "conflicts with --url, --model", "without reading a key", "agents login agent01 opencode proxy --url https://proxy.example --model MODEL"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help lacks %q:\n%s", want, stdout)
		}
	}
	for _, forbidden := range []string{"--wire-api", "--replace-login", "claude", "device-code"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("help includes inapplicable %q:\n%s", forbidden, stdout)
		}
	}
}

// TestAgentLoginHelpSelectionRejectsUnknownNames proves catalog selection errors exit 2 before sandbox lookup through RunWithHost.
func TestAgentLoginHelpSelectionRejectsUnknownNames(t *testing.T) {
	for _, tc := range []struct{ agent, workflow, message string }{
		{"missing", "", `unknown agent "missing"`},
		{"codex", "missing", `unknown workflow "missing"`},
	} {
		args := []string{"agents", "login", "not/a/sandbox", tc.agent}
		if tc.workflow != "" {
			args = append(args, tc.workflow)
		}
		args = append(args, "--help")
		status, stdout, stderr, calls := runHelp(t, args...)
		if status != 2 || stdout != "" || len(calls) != 0 || !strings.Contains(stderr, tc.message) {
			t.Errorf("%v: status=%d stdout=%q stderr=%q calls=%v", args, status, stdout, stderr, calls)
		}
	}
}

// TestAgentLoginValidationNamesApplicableUsage proves invalid options, requirements, and conflicts fail before execution with selected workflow usage through RunWithHost.
func TestAgentLoginValidationNamesApplicableUsage(t *testing.T) {
	for _, tc := range []struct {
		args           []string
		message, usage string
		forbidden      []string
	}{
		{[]string{"claude", "browser", "--url", "https://proxy.example"}, "does not support --url", "agents login NAME claude browser", []string{"[--url", "[--model"}},
		{[]string{"opencode", "proxy", "--url", "https://proxy.example"}, "requires --model", "agents login NAME opencode proxy", []string{"--wire-api", "--replace-login"}},
		{[]string{"opencode", "proxy", "--model", "MODEL"}, "requires --url", "agents login NAME opencode proxy", []string{"--wire-api", "--replace-login"}},
		{[]string{"codex", "proxy", "--remove", "--replace-login"}, "--remove conflicts with --replace-login", "agents login NAME codex proxy", []string{"--wire-api"}},
		{[]string{"copilot", "proxy", "--url", "https://proxy.example", "--model", "MODEL", "--wire-api", "invalid"}, "--wire-api requires completions or responses", "agents login NAME copilot proxy", []string{"--replace-login"}},
		{[]string{"opencode", "proxy", "--remove", "--model", "MODEL"}, "--remove conflicts with --model", "agents login NAME opencode proxy", []string{"--wire-api", "--replace-login"}},
	} {
		args := append([]string{"agents", "login", "agent01"}, tc.args...)
		status, stdout, stderr, calls := runHelp(t, args...)
		if status != 2 || stdout != "" || len(calls) != 0 || !strings.Contains(stderr, tc.message) || !strings.Contains(stderr, "usage: sandboxed-agents "+tc.usage) {
			t.Errorf("%v: status=%d stdout=%q stderr=%q calls=%v", args, status, stdout, stderr, calls)
		}
		for _, forbidden := range tc.forbidden {
			if strings.Contains(stderr, forbidden) {
				t.Errorf("%v: inapplicable %s in %s", args, forbidden, stderr)
			}
		}
	}
}

// unreadableLoginKey fails a test if help attempts to read a secret from stdin.
type unreadableLoginKey struct{ t *testing.T }

// Read reports any attempt to consume help's standard input.
func (r unreadableLoginKey) Read([]byte) (int, error) {
	r.t.Error("help attempted to read a secret")
	return 0, nil
}

// TestAgentLoginHelpIsCatalogOnly proves agent and workflow help need no valid NAME, secret, host files, or runner calls through RunWithHost on Linux and Windows.
func TestAgentLoginHelpIsCatalogOnly(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			f := linuxHost(t)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("LOCALAPPDATA", state)
			t.Setenv("SANDBOXED_AGENTS_GROUP", "invalid/group")
			for _, tc := range []struct {
				agent     string
				workflows []string
				options   []string
				forbidden []string
			}{
				{"claude", []string{"browser", "proxy"}, []string{"--url URL", "--model ID", "--remove"}, []string{"--wire-api", "--replace-login"}},
				{"codex", []string{"browser", "device-code", "proxy"}, []string{"--url URL", "--model ID", "--remove", "--replace-login"}, []string{"--wire-api"}},
				{"copilot", []string{"device-code", "proxy"}, []string{"--url URL", "--model ID", "--remove", "--wire-api API"}, []string{"--replace-login"}},
				{"opencode", []string{"provider", "proxy"}, []string{"--url URL", "--model ID", "--remove"}, []string{"--wire-api", "--replace-login"}},
			} {
				for _, workflow := range append([]string{""}, tc.workflows...) {
					args := []string{"agents", "login", "not/a/sandbox", tc.agent}
					if workflow != "" {
						args = append(args, workflow)
					}
					args = append(args, "--help")
					host := helpHost(t)
					host.Platform = platform
					host.WindowsBuild = func() (uint32, error) { t.Error("help queried Windows build"); return 0, nil }
					host.WindowsWorkstation = func() (bool, error) { t.Error("help queried Windows workstation"); return false, nil }
					host.WindowsNativeArchitecture = func() (uint16, error) { t.Error("help queried Windows architecture"); return 0, nil }
					host.Stdin = unreadableLoginKey{t}
					var stdout, stderr bytes.Buffer
					status := cli.RunWithHost(args, &stdout, &stderr, host)
					if status != 0 || stderr.Len() != 0 {
						t.Fatalf("%v: status=%d stderr=%s", args, status, stderr.String())
					}
					text := stdout.String()
					for _, forbidden := range tc.forbidden {
						if strings.Contains(text, forbidden) {
							t.Errorf("%v: inapplicable %s in %s", args, forbidden, text)
						}
					}
					if workflow == "" || workflow == "proxy" {
						for _, option := range tc.options {
							if !strings.Contains(text, option) {
								t.Errorf("%v: missing %s in %s", args, option, text)
							}
						}
					} else if strings.Contains(text, "--url") || strings.Contains(text, "--model") || strings.Contains(text, "--remove") {
						t.Errorf("%v: native login advertises proxy options: %s", args, text)
					}
					workflows := []string{workflow}
					if workflow == "" {
						workflows = tc.workflows
					}
					for _, selected := range workflows {
						if !strings.Contains(text, "sandboxed-agents agents login agent01 "+tc.agent+" "+selected) {
							t.Errorf("%v: missing example for %s: %s", args, selected, text)
						}
					}
				}
			}
			if calls := testutil.Invocations(t, f.fakes.PodmanLog); len(calls) != 0 {
				t.Fatalf("runner calls: %v", calls)
			}
			if calls := testutil.Invocations(t, f.fakes.SSHLog); len(calls) != 0 {
				t.Fatalf("SSH runner calls: %v", calls)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatalf("help changed host state: %v, %v", entries, err)
			}
		})
	}
}

// TestAgentLoginCatalogOptionDrivesHelpAndValidation proves a workflow option added to the catalog is parsed, documented and enforced without another help table edit through Parse and RunWithHost.
func TestAgentLoginCatalogOptionDrivesHelpAndValidation(t *testing.T) {
	previous := catalog.Default.Agents
	catalog.Default.Agents = make(map[string]catalog.Agent)
	for name, agent := range previous {
		catalog.Default.Agents[name] = agent
	}
	t.Cleanup(func() { catalog.Default.Agents = previous })
	catalog.Default.Agents["sample"] = catalog.Agent{Delivered: true, LoginWorkflows: map[string]catalog.AgentLoginWorkflow{
		"token": {Options: []catalog.AgentLoginOption{
			{Name: "endpoint", Value: "URL", Description: "Token endpoint.", Required: true, RequiredUnless: "clear"},
			{Name: "clear", Description: "Clear token.", Conflicts: []string{"endpoint"}},
		}, Examples: []string{"--endpoint https://login.example", "--clear"}},
	}}
	status, stdout, stderr, calls := runHelp(t, "agents", "login", "missing", "sample", "token", "--help")
	for _, want := range []string{"--endpoint URL", "Required unless --clear", "conflicts with --endpoint", "--endpoint https://login.example"} {
		if status != 0 || stderr != "" || len(calls) != 0 || !strings.Contains(stdout, want) {
			t.Errorf("help missing %s: status=%d stderr=%q calls=%v stdout=%s", want, status, stderr, calls, stdout)
		}
	}
	for _, tail := range [][]string{{"--endpoint", "https://login.example"}, {"--clear"}} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo", "sample", "token"}, tail...), "default"); err != nil {
			t.Errorf("valid option rejected: %v", err)
		}
	}
	for _, tail := range [][]string{{}, {"--clear", "--endpoint", "https://login.example"}, {"--url", "https://proxy.example"}} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo", "sample", "token"}, tail...), "default"); err == nil {
			t.Errorf("accepted invalid options %v", tail)
		}
	}
}
