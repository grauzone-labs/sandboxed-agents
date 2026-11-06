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

// TestProxyWorkflowReadsKeyOnlyOnStdin proves first-line input reaches only manager stdin without a terminal through RunWithHost.
func TestProxyWorkflowReadsKeyOnlyOnStdin(t *testing.T) {
	f := sandboxStateFixture(t)
	envLog := filepath.Join(t.TempDir(), "podman-environment.json")
	t.Setenv("TEST_PODMAN_ENV_LOG", envLog)
	f.host.Stdin = strings.NewReader("\r\t  sentinel-proxy-key  \r\nignored\n")
	f.host.IsInputTerminal = func(io.Reader) bool { return false }
	f.host.IsOutputTerminal = func(io.Writer) bool { return false }
	f.responses[agentWorkProbe] = response{Output: "sandboxed-agents-manager development\n"}
	f.responses[agentWorkStatus+"claude"] = response{Output: `{"name":"claude","enabled":true,"installed_version":"1.2.3"}`}
	command := "exec --user=1000:1000 --env HOME=/home/agent -i " + shellContainer + " " + integrationManager + " agents login claude proxy --url http://proxy.example:8317 --model example-model"
	f.responses[command] = response{ExpectedStdin: "sentinel-proxy-key\n", EchoStdin: true}
	status, output, calls := f.run(t, "agents", "login", "agent01", "claude", "proxy", "--url", "http://proxy.example:8317/", "--model", "example-model")
	if status != 0 || !strings.Contains(output, "unencrypted") {
		t.Fatalf("status=%d output=%s calls=%v", status, output, calls)
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
