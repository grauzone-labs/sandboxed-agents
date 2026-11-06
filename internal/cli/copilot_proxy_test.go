package cli_test

import (
	"io"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestCopilotProxyGrammar proves required model and wire API validation and explicit workflow selection at Parse and RunWithHost.
func TestCopilotProxyGrammar(t *testing.T) {
	for _, wire := range []string{"completions", "responses"} {
		r, err := cli.Parse([]string{"agents", "login", "demo", "copilot", "proxy", "--url", "https://proxy.example/", "--model", "chosen", "--wire-api", wire}, "default")
		if err != nil || r.Options["url"] != "https://proxy.example" {
			t.Fatalf("valid proxy: %v %v", r, err)
		}
	}
	for _, tail := range [][]string{
		{"copilot"},
		{"copilot", "proxy", "--url", "https://proxy.example"},
		{"copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen", "--wire-api", "invalid"},
		{"copilot", "proxy", "--remove", "--wire-api", "responses"},
		{"copilot", "device-code", "--wire-api", "responses"},
		{"claude", "proxy", "--url", "https://proxy.example", "--wire-api", "responses"},
	} {
		f := sandboxStateFixture(t)
		code, _, calls := f.run(t, append([]string{"agents", "login", "demo"}, tail...)...)
		if code != 2 || len(calls) != 0 {
			t.Fatalf("%v: code=%d calls=%v", tail, code, calls)
		}
	}
	_, err := cli.Parse([]string{"agents", "login", "demo", "copilot"}, "default")
	if err == nil || !strings.Contains(err.Error(), "device-code") || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("missing workflow choices: %v", err)
	}
}

// TestCopilotProxyVersionBeforeKey proves old or unverified installations are refused before key input through RunWithHost.
func TestCopilotProxyVersionBeforeKey(t *testing.T) {
	for _, version := range []string{"1.0.105", "1.0.106-beta.1", "invalid"} {
		f := sandboxStateFixture(t)
		f.host.Stdin = unreadableToken{t}
		f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
		f.responses[agentWorkStatus+"copilot"] = response{Output: `{"name":"copilot","enabled":true,"installed_version":"` + version + `","pin":"` + version + `"}`}
		code, out, _ := f.run(t, "agents", "login", "agent01", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen")
		if code != 1 || !strings.Contains(out, "1.0.106") || !strings.Contains(out, "agents enable") || !strings.Contains(out, "agents update") {
			t.Fatalf("version=%s code=%d output=%s", version, code, out)
		}
	}
}

// TestCopilotProxyTransport proves validated wire options reach the manager while the key travels only on stdin through RunWithHost.
func TestCopilotProxyTransport(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = strings.NewReader("proxy-secret\nignored")
	f.host.IsInputTerminal = func(_ io.Reader) bool { return false }
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"copilot"] = response{Output: `{"name":"copilot","enabled":true,"installed_version":"1.0.106"}`}
	command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login copilot proxy --url https://proxy.example --model chosen --wire-api responses"
	f.responses[command] = response{ExpectedStdin: "proxy-secret\n", EchoStdin: true}
	code, out, calls := f.run(t, "agents", "login", "agent01", "copilot", "proxy", "--url", "https://proxy.example", "--model", "chosen", "--wire-api", "responses")
	if code != 0 || strings.Contains(out, "proxy-secret") {
		t.Fatalf("code=%d output=%s", code, out)
	}
	found := false
	for _, call := range calls {
		if strings.Contains(strings.Join(call, " "), "proxy-secret") {
			t.Fatal("key in arguments")
		}
		if strings.Join(call, " ") == command {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing mutation call: %v", calls)
	}
}
