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
	"sync/atomic"
	"testing"
	"time"
)

// TestOpenCodeProxyNativeClientReachesOnlyFakeUpstream proves the real OpenCode CLI uses the configured chat endpoint, bearer key and model through RunWithEffects.
func TestOpenCodeProxyNativeClientReachesOnlyFakeUpstream(t *testing.T) {
	binary := os.Getenv("SANDBOXED_AGENTS_TEST_OPENCODE")
	if binary == "" {
		t.Skip("set SANDBOXED_AGENTS_TEST_OPENCODE to the installed catalog executable")
	}
	var requests atomic.Int32
	var registryChecks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "CONNECT" && r.Host == "registry.npmjs.org:443" {
			registryChecks.Add(1)
			http.Error(w, "registry deliberately unavailable in isolated test", http.StatusForbidden)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/prefix/v1/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected endpoint", 403)
			return
		}
		if r.Header.Get("Authorization") != "Bearer local-test-secret" {
			t.Error("incorrect bearer key")
		}
		var body struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "chosen" {
			t.Errorf("model=%q", body.Model)
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"id":"test","object":"chat.completion.chunk","created":1,"model":"chosen","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, "data: "+`{"id":"test","object":"chat.completion.chunk","created":1,"model":"chosen","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	home, version, installs := t.TempDir(), "1.2.3", 0
	e := agentEffects(t, home, &version, &installs)
	runAgents(t, e, 0, "enable", "opencode")
	e.Stdin = strings.NewReader("local-test-secret")
	runAgents(t, e, 0, "login", "opencode", "proxy", "--url", server.URL+"/prefix", "--model", "chosen")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "Reply OK")
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "NO_PROXY=127.0.0.1,localhost", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_DEFAULT_PLUGINS=1"}
	output, err := cmd.CombinedOutput()
	if err != nil || requests.Load() == 0 || !strings.Contains(string(output), "OK") {
		t.Fatalf("native client: %v requests=%d output=%s", err, requests.Load(), output)
	}
	t.Logf("OpenCode native contract: %d authenticated chat requests; %d denied registry checks; no provider-host requests", requests.Load(), registryChecks.Load())
}
