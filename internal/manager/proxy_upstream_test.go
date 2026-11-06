package manager_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClaudeProxyNativeClientReachesOnlyFakeUpstream proves the installed catalog Claude CLI accepts manager settings and metadata and sends the configured bearer token, model and prefixed messages path through RunWithEffects and a real isolated client.
func TestClaudeProxyNativeClientReachesOnlyFakeUpstream(t *testing.T) {
	binary := os.Getenv("SANDBOXED_AGENTS_TEST_CLAUDE")
	if binary == "" {
		t.Skip("set SANDBOXED_AGENTS_TEST_CLAUDE to the installed upstream Claude executable for the native contract test")
	}
	var err error
	var mu sync.Mutex
	var requests []string
	messages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.Host+r.URL.Path)
		if r.Method == "CONNECT" || (r.URL.IsAbs() && r.URL.Host != r.Host) || strings.Contains(r.Host, "anthropic") {
			t.Errorf("external request blocked: %s %s", r.Method, r.Host)
			http.Error(w, "external requests forbidden", 403)
			return
		}
		if r.URL.Path != "/prefix/v1/messages" {
			http.Error(w, "unsupported fake endpoint", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer local-test-secret" {
			t.Error("incorrect bearer authentication")
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "claude-sonnet-4-20250514" {
			t.Errorf("model=%s", body.Model)
		}
		messages++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_fake","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "claude")
	e.Stdin = strings.NewReader("  local-test-secret \r\n")
	runAgents(t, e, 0, "login", "claude", "proxy", "--url", server.URL+"/prefix", "--model", "claude-sonnet-4-20250514")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-p", "Reply OK", "--max-turns", "1", "--tools", "")
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "NO_PROXY=127.0.0.1,localhost", "API_TIMEOUT_MS=10000"}
	probe := exec.CommandContext(ctx, binary, "auth", "status", "--json")
	probe.Dir, probe.Env = home, cmd.Env
	probeOutput, probeErr := probe.Output()
	var auth struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(probeOutput, &auth); err != nil {
		t.Fatalf("invalid auth status: %v", err)
	}
	if probeErr != nil && (auth.LoggedIn || probe.ProcessState.ExitCode() != 1) {
		t.Fatalf("auth probe failed: %v", probeErr)
	}
	mu.Lock()
	probeRequests := len(requests)
	mu.Unlock()
	if probeRequests != 0 {
		t.Fatal("authentication status contacted upstream")
	}
	t.Logf("native auth status with proxy key only: loggedIn=%t; no server requests", auth.LoggedIn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Claude failed: %v output=%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if messages == 0 || !strings.Contains(string(output), "OK") {
		t.Fatalf("no successful native messages request: requests=%v output=%s", requests, output)
	}
	t.Logf("native Claude accepted ownership metadata; %d messages request(s), observed requests=%v", messages, requests)
}
