package manager_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCopilotProxyNativeClient proves the real catalog Copilot sends streaming tool-enabled requests using the configured provider without leaking GitHub credentials through RunWithEffects and both HTTP wire APIs.
func TestCopilotProxyNativeClient(t *testing.T) {
	binary := os.Getenv("SANDBOXED_AGENTS_TEST_COPILOT")
	if binary == "" {
		t.Skip("set SANDBOXED_AGENTS_TEST_COPILOT to Copilot 1.0.106 for the native contract test")
	}
	versionContext, versionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer versionCancel()
	versionCommand := exec.CommandContext(versionContext, binary, "--version")
	versionCommand.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"), "CI=true", "COPILOT_AUTO_UPDATE=false"}
	versionOutput, err := versionCommand.CombinedOutput()
	if err != nil || !strings.Contains(string(versionOutput), "GitHub Copilot CLI 1.0.106.") {
		t.Fatalf("contract test requires version floor 1.0.106: %v %s", err, versionOutput)
	}
	t.Log("native client: GitHub Copilot CLI 1.0.106")
	for _, scenario := range []struct {
		wire   string
		github bool
	}{
		{"completions", false}, {"responses", false}, {"completions", true}, {"responses", true},
	} {
		wire := scenario.wire
		t.Run(fmt.Sprintf("%s/github-token=%t", wire, scenario.github), func(t *testing.T) {
			var mu sync.Mutex
			calls, accountQueries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				expected := "/prefix/v1/chat/completions"
				if wire == "responses" {
					expected = "/prefix/v1/responses"
				}
				// A signed-in CLI probes GitHub account metadata even with a BYOK model.
				// Block that query locally; inference hosts are never permitted.
				if r.Method == "CONNECT" && r.Host == "api.github.com:443" {
					accountQueries++
					http.Error(w, "account metadata blocked", 403)
					return
				}
				if r.Method != "POST" || r.URL.Path != expected {
					t.Errorf("unexpected outbound request: %s %s", r.Method, r.URL)
					http.Error(w, "blocked", 403)
					return
				}
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if strings.Contains(string(b), "github-sentinel") || strings.Contains(fmt.Sprint(r.Header), "github-sentinel") || strings.Contains(r.URL.String(), "github-sentinel") {
					t.Error("GitHub token leaked")
				}
				if r.Header.Get("Authorization") != "Bearer local-test-secret" {
					t.Error("wrong bearer key")
				}
				var request struct {
					Model  string
					Stream bool
					Tools  []json.RawMessage
				}
				if json.Unmarshal(b, &request) != nil || request.Model != "chosen" || !request.Stream || len(request.Tools) == 0 {
					t.Errorf("invalid request: %s", b)
				}
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				if wire == "completions" {
					io.WriteString(w, "data: {\"id\":\"chatcmpl_fake\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"chosen\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl_fake\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"chosen\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					events := []string{
						`{"type":"response.created","sequence_number":0,"response":{"id":"resp_fake","object":"response","status":"in_progress","model":"chosen","output":[]}}`,
						`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_fake","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
						`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_fake","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
						`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_fake","output_index":0,"content_index":0,"delta":"OK"}`,
						`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg_fake","output_index":0,"content_index":0,"text":"OK"}`,
						`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg_fake","output_index":0,"content_index":0,"part":{"type":"output_text","text":"OK","annotations":[]}}`,
						`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_fake","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}}`,
						`{"type":"response.completed","sequence_number":7,"response":{"id":"resp_fake","object":"response","status":"completed","model":"chosen","output":[{"id":"msg_fake","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
					}
					for _, event := range events {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}
			}))
			defer server.Close()
			home, version, installs := t.TempDir(), "1.0.106", 0
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "copilot")
			e.Stdin = strings.NewReader("local-test-secret\n")
			runAgents(t, e, 0, "login", "copilot", "proxy", "--url", server.URL+"/prefix", "--model", "chosen", "--wire-api", wire)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-p", "Reply OK", "--model", "cliproxyapi/chosen", "--disable-client-telemetry", "--disable-builtin-mcps", "--no-auto-update", "--stream", "on")
			cmd.Dir = home
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CI=true", "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "ALL_PROXY=" + server.URL, "NO_PROXY=127.0.0.1,localhost"}
			if scenario.github {
				cmd.Env = append(cmd.Env, "GH_TOKEN=github-sentinel")
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native Copilot failed: %v\n%s", err, output)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls == 0 || !strings.Contains(string(output), "OK") {
				t.Fatalf("no successful inference: calls=%d output=%s", calls, output)
			}
			if (accountQueries > 0) != scenario.github {
				t.Fatalf("GitHub credential use not observed as expected: token=%t account queries=%d", scenario.github, accountQueries)
			}
			t.Logf("Copilot completed %s with %d streaming tool-enabled request(s), %d blocked GitHub account queries", wire, calls, accountQueries)
		})
	}
}
