package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grauzone-labs/sandboxed-agents/internal/cli"
)

// TestProxyDisclosureSurvivesSecretRedaction proves successful setup discloses the skipped server check without emitting the key anywhere through RunWithHost.
func TestProxyDisclosureSurvivesSecretRedaction(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode"} {
		for _, tc := range []struct{ key, notice string }{
			{"not", "SERVER KEY CHECK SKIPPED\n"},
			{"checked", "SERVER KEY CHECK SKIPPED\n"},
			{"server", "SERVER KEY CHECK SKIPPED\n"},
			{"e", "SERVER KEY CHECK SKIPPED\n"},
			{".", "SERVER KEY CHECK SKIPPED\n"},
			{"[redacted]", "the key was not checked against the server."},
			{"]x", "the key was not checked against the server."},
			{"sentinel-proxy-key", "the key was not checked against the server."},
		} {
			t.Run(agent+"/"+tc.key, func(t *testing.T) {
				key := tc.key
				url := "https://proxy.example"
				if key == "e" {
					url = "http://proxy.example"
				}
				f := sandboxStateFixture(t)
				f.host.Stdin = strings.NewReader(key + "\n")
				f.host.IsInputTerminal = func(io.Reader) bool { return false }
				f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
				f.responses[agentWorkStatus+agent] = response{Output: fmt.Sprintf(`{"name":%q,"enabled":true,"installed_version":"1.2.3"}`, agent)}
				command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login " + agent + " proxy --url " + url + " --model chosen"
				f.responses[command] = response{
					ExpectedStdin: key + "\n",
					Output:        "configuration written; the key was not checked against the server.\npayload=" + key + "x\n",
					Stderr:        "diagnostic=" + key + "\n",
				}
				status, output, _ := f.run(t, "agents", "login", "agent01", agent, "proxy", "--url", url, "--model", "chosen")
				if strings.Contains(output, key) || !strings.Contains(output, "payload=") || !strings.Contains(output, "diagnostic=") {
					t.Fatalf("complete output leaked the key or dropped manager content: %q", output)
				}
				if status != 0 || strings.Count(output, tc.notice) != 1 {
					t.Fatalf("unchecked-key disclosure missing or repeated: status=%d output=%q", status, output)
				}
				if key == "e" && !strings.Contains(output, "WARNING THE PROXY KEY TRAVELS UNENCRYPTED OVER HTTP\n") {
					t.Fatalf("HTTP warning is not readable: %q", output)
				}
			})
		}
	}
}

// TestProxyFailureExcludesSecret proves refused setup reports a readable failure without the accepted key through RunWithHost.
func TestProxyFailureExcludesSecret(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "copilot", "opencode"} {
		for _, key := range []string{"proxy", "configuration", "failed", "P", "F", "[redacted]", "]x", "sentinel-proxy-key"} {
			t.Run(agent+"/"+key, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.host.Stdin = strings.NewReader(key + "\n")
				f.host.IsInputTerminal = func(io.Reader) bool { return false }
				f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
				f.responses[agentWorkStatus+agent] = response{Output: fmt.Sprintf(`{"name":%q,"enabled":true,"installed_version":"1.2.3"}`, agent)}
				command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login " + agent + " proxy --url https://proxy.example --model chosen"
				f.responses[command] = response{Exit: 23, ExpectedStdin: key + "\n", Stderr: "provided=" + key + "x\n"}
				status, output, _ := f.run(t, "agents", "login", "agent01", agent, "proxy", "--url", "https://proxy.example", "--model", "chosen")
				if status != 1 || strings.Contains(output, key) || !strings.Contains(output, "provided=") {
					t.Fatalf("failure leaked key or lost status/content: status=%d output=%q", status, output)
				}
				if !strings.Contains(strings.ToLower(output), "proxy configuration failed\n") || strings.Contains(output, "SERVER KEY CHECK SKIPPED") {
					t.Fatalf("failure lacks readable error or claims success: %q", output)
				}
			})
		}
	}
}

// TestProxyDisclosureExcludesFailureAndRemoval proves the host adds no setup notice after a refused write or keyless removal through RunWithHost.
func TestProxyDisclosureExcludesFailureAndRemoval(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("remove=%t", remove), func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.host.Stdin = strings.NewReader("not\n")
			f.host.IsInputTerminal = func(io.Reader) bool { return false }
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[agentWorkStatus+"copilot"] = response{Output: `{"name":"copilot","enabled":true,"installed_version":"1.0.106"}`}
			options := []string{"--url", "https://proxy.example", "--model", "chosen"}
			answer := response{Exit: 23, ExpectedStdin: "not\n", Stderr: "refused provided=not\n"}
			wantStatus := 1
			if remove {
				f.host.Stdin = unreadableToken{t}
				options = []string{"--remove"}
				answer = response{Output: "configuration removed\n"}
				wantStatus = 0
			}
			command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login copilot proxy " + strings.Join(options, " ")
			f.responses[command] = answer
			status, output, _ := f.run(t, append([]string{"agents", "login", "agent01", "copilot", "proxy"}, options...)...)
			if status != wantStatus || strings.Contains(output, "the key was not checked against the server.") || strings.Contains(output, "SERVER KEY CHECK SKIPPED") {
				t.Fatalf("unexpected setup notice or status: status=%d output=%q", status, output)
			}
			if !remove && (!strings.Contains(output, "provided= [redacted] ") || strings.Contains(output, "provided=not")) {
				t.Fatalf("failure leaked the key: %q", output)
			}
		})
	}
}

// TestProxyOutputFailureExcludesSecret proves output failures retain a readable key-free error through RunWithHost.
func TestProxyOutputFailureExcludesSecret(t *testing.T) {
	for _, phase := range []string{"warning", "stdout", "stderr"} {
		for _, key := range []string{"report", "configuration", "C"} {
			t.Run(phase+"/"+key, func(t *testing.T) {
				f := sandboxStateFixture(t)
				f.host.Stdin = strings.NewReader(key + "\n")
				f.host.IsInputTerminal = func(io.Reader) bool { return false }
				f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
				f.responses[agentWorkStatus+"copilot"] = response{Output: `{"name":"copilot","enabled":true,"installed_version":"1.0.106"}`}
				url := "https://proxy.example"
				if phase == "warning" {
					url = "http://proxy.example"
				}
				command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login copilot proxy --url " + url + " --model chosen"
				f.responses[command] = response{ExpectedStdin: key + "\n", Output: "result\n", Stderr: "diagnostic\n"}
				data, err := json.Marshal(f.responses)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("TEST_PODMAN_RESPONSES", string(data))
				var output strings.Builder
				var failed proxyFailOnceWriter
				var stdout, stderr io.Writer = &output, &failed
				if phase == "stdout" {
					stdout, stderr = &failed, &output
				}
				status := cli.RunWithHost([]string{"agents", "login", "agent01", "copilot", "proxy", "--url", url, "--model", "chosen"}, stdout, stderr, f.host)
				combined := output.String() + failed.output.String()
				if status != 1 || strings.Contains(combined, key) || !strings.Contains(strings.ToLower(combined), "cannot report proxy configuration result\n") {
					t.Fatalf("output failure lost error or leaked key: status=%d output=%q", status, combined)
				}
			})
		}
	}
}

// proxyFailOnceWriter rejects the first output write and captures later error reporting.
type proxyFailOnceWriter struct {
	output strings.Builder
	failed bool
}

// Write fails once before accepting subsequent diagnostic bytes.
func (w *proxyFailOnceWriter) Write(data []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.output.Write(data)
}

// TestProxyWorkflowGrammar proves proxy options normalize URLs and reject invalid combinations at cli.Parse.
func TestProxyWorkflowGrammar(t *testing.T) {
	r, err := cli.Parse([]string{"agents", "login", "demo", "claude", "proxy", "--url", "http://proxy.example:8317/prefix///", "--model", "example-model"}, "default")
	if err != nil || r.Options["url"] != "http://proxy.example:8317/prefix" {
		t.Fatalf("request=%+v error=%v", r, err)
	}
	for _, tail := range [][]string{
		{"claude"}, {"claude", "proxy"}, {"claude", "browser", "--url", "https://proxy.example"},
		{"codex", "browser", "--model", "x"}, {"claude", "proxy", "--remove", "--url", "https://proxy.example"},
		{"claude", "proxy", "--remove", "--model", "x"},
	} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo"}, tail...), "default"); err == nil {
			t.Fatalf("accepted %q", tail)
		}
	}
	for _, url := range []string{"", "ftp://example.com", "https://user:pass@example.com", "https://example.com?", "https://example.com#", "https://example.com/v1/", "http://", "https://example.com/%0a", "https://example.com:99999"} {
		if _, err := cli.Parse([]string{"agents", "login", "demo", "claude", "proxy", "--url", url}, "default"); err == nil {
			t.Fatalf("accepted URL %q", url)
		}
	}
}

// TestProxyWorkflowReadsKeyOnlyOnStdin proves Claude and OpenCode forward first-line keys only to manager stdin with an HTTP warning through non-terminal RunWithHost.
func TestProxyWorkflowReadsKeyOnlyOnStdin(t *testing.T) {
	for _, agent := range []string{"claude", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			f := sandboxStateFixture(t)
			envLog := filepath.Join(t.TempDir(), "podman-environment.json")
			t.Setenv("TEST_PODMAN_ENV_LOG", envLog)
			f.host.Stdin = strings.NewReader("\r\t  sentinel-proxy-key  \r\nignored\n")
			f.host.IsInputTerminal = func(io.Reader) bool { return false }
			f.host.IsOutputTerminal = func(io.Writer) bool { return false }
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[agentWorkStatus+agent] = response{Output: fmt.Sprintf(`{"name":%q,"enabled":true,"installed_version":"1.2.3"}`, agent)}
			command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login " + agent + " proxy --url http://proxy.example:8317 --model example-model"
			f.responses[command] = response{ExpectedStdin: "sentinel-proxy-key\n", EchoStdin: true}
			status, output, calls := f.run(t, "agents", "login", "agent01", agent, "proxy", "--url", "http://proxy.example:8317/", "--model", "example-model")
			if status != 0 || !strings.Contains(output, "unencrypted") {
				t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
			}
			commandSeen := false
			for _, call := range calls {
				if strings.Join(call, " ") == command {
					commandSeen = true
				}
			}
			if !commandSeen {
				t.Fatalf("manager command missing: %v", calls)
			}
			environment, err := os.ReadFile(envLog)
			if err != nil {
				t.Fatal(err)
			}
			var entries []string
			if err := json.Unmarshal(environment, &entries); err != nil {
				t.Fatal(err)
			}
			for _, entry := range append(entries, os.Environ()...) {
				// The fake's response script contains the expected stdin sentinel by design.
				if strings.HasPrefix(entry, "TEST_PODMAN_RESPONSES=") {
					continue
				}
				if strings.Contains(entry, "sentinel-proxy-key") {
					t.Fatal("key leaked into process environment")
				}
			}
			if strings.Contains(fmt.Sprint(calls)+output, "sentinel-proxy-key") {
				t.Fatal("key leaked")
			}
		})
	}
}

// TestProxyWorkflowRejectsBadKeys proves invalid input never reaches the manager mutation through RunWithHost.
func TestProxyWorkflowRejectsBadKeys(t *testing.T) {
	for _, key := range []string{"", "\n", "a b\n", "a\tb\n", "a\rb\n", "a\x00b\n", "a\x1bb\n", "\x00key\n", "key\x00\n", "\x1bkey\n", "key\x1b\n"} {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			f := sandboxStateFixture(t)
			f.host.Stdin = strings.NewReader(key)
			f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
			f.responses[agentWorkStatus+"claude"] = response{Output: `{"name":"claude","enabled":true,"installed_version":"1.2.3"}`}
			status, _, calls := f.run(t, "agents", "login", "agent01", "claude", "proxy", "--url", "https://proxy.example")
			if status != 1 {
				t.Fatalf("status=%d", status)
			}
			for _, call := range calls {
				if strings.Contains(strings.Join(call, " "), "agents login") {
					t.Fatal("mutation ran for invalid key")
				}
			}
		})
	}
}

// TestProxyRemoveReadsNoKey proves removal bypasses input and sends only the remove flag through RunWithHost.
func TestProxyRemoveReadsNoKey(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = unreadableToken{t}
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"claude"] = response{Output: `{"name":"claude","enabled":true,"installed_version":"1.2.3"}`}
	command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login claude proxy --remove"
	f.responses[command] = response{}
	status, output, _ := f.run(t, "agents", "login", "agent01", "claude", "proxy", "--remove")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
}

// TestProxyChecksBeforeInput proves sandbox failure precedes any key input through RunWithHost.
func TestProxyChecksBeforeInput(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = unreadableToken{t}
	f.responses[agentWorkProbe] = response{Exit: 125}
	status, _, _ := f.run(t, "agents", "login", "agent01", "claude", "proxy", "--url", "https://proxy.example")
	if status != 1 {
		t.Fatalf("status=%d", status)
	}
}

// TestProxyUsageBeforePodman proves invalid options exit two before host queries at RunWithHost.
func TestProxyUsageBeforePodman(t *testing.T) {
	for _, tail := range [][]string{{"proxy"}, {"browser", "--url", "https://example.com"}, {"browser", "--model", "x"}, {"proxy", "--url", "ftp://example.com"}, {"proxy", "--remove", "--model", "x"}} {
		f := sandboxStateFixture(t)
		f.host.Stdin = unreadableToken{t}
		status, _, calls := f.run(t, append([]string{"agents", "login", "agent01", "claude"}, tail...)...)
		if status != 2 || len(calls) != 0 {
			t.Fatalf("tail=%v status=%d calls=%v", tail, status, calls)
		}
	}
}

// TestProxyPreservesEncodedPathPrefix proves URL normalization does not change an encoded proxy routing prefix at cli.Parse.
func TestProxyPreservesEncodedPathPrefix(t *testing.T) {
	r, err := cli.Parse([]string{"agents", "login", "demo", "claude", "proxy", "--url", "https://proxy.example/route%2Ftenant///"}, "default")
	if err != nil || r.Options["url"] != "https://proxy.example/route%2Ftenant" {
		t.Fatalf("url=%q err=%v", r.Options["url"], err)
	}
}

// TestOpenCodeProxyGrammar proves explicit workflow and model requirements at cli.Parse.
func TestOpenCodeProxyGrammar(t *testing.T) {
	for _, tail := range [][]string{{}, {"proxy", "--url", "https://proxy.example"}, {"provider", "--model", "chosen"}} {
		_, err := cli.Parse(append([]string{"agents", "login", "demo", "opencode"}, tail...), "default")
		if err == nil {
			t.Fatalf("accepted %v", tail)
		}
		if len(tail) == 0 && (!strings.Contains(err.Error(), "provider") || !strings.Contains(err.Error(), "proxy")) {
			t.Fatal(err)
		}
	}
	for _, tail := range [][]string{{"provider"}, {"proxy", "--url", "https://proxy.example", "--model", "chosen"}, {"proxy", "--remove"}} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo", "opencode"}, tail...), "default"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCodexProxyGrammar proves Codex requires a model and reserves replacement for its proxy workflow at Parse.
func TestCodexProxyGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"codex", "proxy", "--url", "https://example"},
		{"codex", "proxy", "--remove", "--replace-login"},
		{"codex", "browser", "--replace-login"},
		{"claude", "proxy", "--url", "https://example", "--replace-login"},
		{"opencode", "proxy", "--url", "https://example", "--model", "chosen", "--replace-login"},
	} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo"}, args...), "default"); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, tail := range [][]string{{"proxy", "--url", "https://example", "--model", "chosen", "--replace-login"}, {"proxy", "--remove"}} {
		if _, err := cli.Parse(append([]string{"agents", "login", "demo", "codex"}, tail...), "default"); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCodexProxyVersionPrecedesKey proves the installed-version gate runs before host key input through RunWithHost.
func TestCodexProxyVersionPrecedesKey(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = unreadableToken{t}
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"codex"] = response{Output: `{"name":"codex","enabled":true,"installed_version":"0.1.0"}`}
	status, output, _ := f.run(t, "agents", "login", "agent01", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen")
	if status != 1 || !strings.Contains(output, "agents update") {
		t.Fatalf("status=%d output=%s", status, output)
	}
}

// TestCodexProxyRemoveAllowsOlderVersion proves removal reaches the manager without key input for an older installed Codex through RunWithHost.
func TestCodexProxyRemoveAllowsOlderVersion(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = unreadableToken{t}
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"codex"] = response{Output: `{"name":"codex","enabled":true,"installed_version":"0.1.0"}`}
	command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login codex proxy --remove"
	f.responses[command] = response{}
	status, output, calls := f.run(t, "agents", "login", "agent01", "codex", "proxy", "--remove")
	if status != 0 || !strings.Contains(fmt.Sprint(calls), command) {
		t.Fatalf("removal did not reach manager: status=%d output=%s", status, output)
	}
}

// TestCodexProxyHostTransport proves replacement options and key-only stdin reach the manager through RunWithHost.
func TestCodexProxyHostTransport(t *testing.T) {
	f := sandboxStateFixture(t)
	f.host.Stdin = strings.NewReader("codex-secret")
	f.host.IsInputTerminal = func(io.Reader) bool { return false }
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"codex"] = response{Output: `{"name":"codex","enabled":true,"installed_version":"0.166.0"}`}
	command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login codex proxy --replace-login --url https://proxy.example --model chosen"
	f.responses[command] = response{ExpectedStdin: "codex-secret\n", EchoStdin: true}
	status, output, calls := f.run(t, "agents", "login", "agent01", "codex", "proxy", "--url", "https://proxy.example", "--model", "chosen", "--replace-login")
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, output)
	}
	if strings.Contains(fmt.Sprint(calls)+output, "codex-secret") {
		t.Fatal("key leaked")
	}
}
