package manager_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
		wire string
		auth string
	}{
		{"completions", "none"}, {"responses", "none"},
		{"completions", "environment"}, {"responses", "environment"},
		{"completions", "device-code"}, {"responses", "device-code"},
	} {
		wire := scenario.wire
		t.Run(fmt.Sprintf("%s/auth=%s", wire, scenario.auth), func(t *testing.T) {
			var mu sync.Mutex
			calls, accountQueries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if strings.Contains(string(b), "github-sentinel") || strings.Contains(fmt.Sprint(r.Header), "github-sentinel") || strings.Contains(r.URL.String(), "github-sentinel") {
					t.Error("GitHub token leaked")
				}
				expected := "/prefix/v1/chat/completions"
				if wire == "responses" {
					expected = "/prefix/v1/responses"
				}
				// A signed-in CLI probes GitHub account metadata even with a BYOK model.
				// Block that query locally; inference hosts are never permitted.
				if r.Method == "CONNECT" && r.Host == "api.github.com:443" {
					accountQueries++
					http.Error(w, "account metadata blocked", http.StatusForbidden)
					return
				}
				if r.Method != "POST" || r.URL.Path != expected {
					t.Errorf("unexpected outbound request: %s %s", r.Method, r.URL)
					http.Error(w, "blocked", http.StatusForbidden)
					return
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
			var storedAccountQueries atomic.Int64
			var certPath string
			if scenario.auth == "device-code" {
				certPath = copilotDeviceCodeLogin(t, binary, home, &storedAccountQueries)
			}
			e := agentEffects(t, home, &version, &installs)
			runAgents(t, e, 0, "enable", "copilot")
			e.Stdin = strings.NewReader("local-test-secret\n")
			runAgents(t, e, 0, "login", "copilot", "proxy", "--url", server.URL+"/prefix", "--model", "chosen", "--wire-api", wire)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-p", "Reply OK", "--model", "cliproxyapi/chosen", "--disable-client-telemetry", "--disable-builtin-mcps", "--no-auto-update", "--stream", "on")
			cmd.Dir = home
			cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CI=true", "HTTP_PROXY=" + server.URL, "HTTPS_PROXY=" + server.URL, "ALL_PROXY=" + server.URL, "NO_PROXY=127.0.0.1,localhost"}
			if scenario.auth == "environment" {
				cmd.Env = append(cmd.Env, "GH_TOKEN=github-sentinel")
			}
			if certPath != "" {
				cmd.Env = append(cmd.Env, "NODE_EXTRA_CA_CERTS="+certPath, "SSL_CERT_FILE="+certPath)
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
			if (accountQueries+int(storedAccountQueries.Load()) > 0) != (scenario.auth != "none") {
				t.Fatalf("GitHub credential use not observed as expected: auth=%s account queries=%d stored account queries=%d", scenario.auth, accountQueries, storedAccountQueries.Load())
			}
			t.Logf("Copilot completed %s with %d streaming tool-enabled request(s), %d blocked GitHub account queries, %d stored-credential account queries", wire, calls, accountQueries, storedAccountQueries.Load())
		})
	}
}

// copilotDeviceCodeLogin runs the native device-code flow against a local TLS identity fixture and counts subsequent uses of its stored dummy credential.
func copilotDeviceCodeLogin(t *testing.T, binary, home string, accountQueries *atomic.Int64) string {
	t.Helper()
	var loginComplete atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			io.WriteString(w, `{"device_code":"local-device","user_code":"LOCAL","verification_uri":"https://127.0.0.1/local-only","expires_in":900,"interval":1}`)
		case "/login/oauth/access_token":
			io.WriteString(w, `{"access_token":"github-sentinel","token_type":"bearer","scope":"read:user"}`)
		case "/copilot_internal/user", "/copilot_internal/managed_settings":
			if !strings.Contains(r.Header.Get("Authorization"), "github-sentinel") {
				t.Error("native client did not send its stored credential to the identity fixture")
			}
			accountQueries.Add(1)
			if loginComplete.Load() {
				http.Error(w, "account metadata blocked", http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"login":"local-fixture","id":123}`)
		default:
			t.Errorf("unexpected identity request: %s %s", r.Method, r.URL)
			http.Error(w, "blocked", http.StatusForbidden)
		}
	}))
	t.Cleanup(server.Close)
	certPath := filepath.Join(home, "identity-ca.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".copilot", "settings.json"), []byte(`{"storeTokenPlaintext":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "login", "--device-code", "--host", server.URL)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CI=true", "COPILOT_AUTO_UPDATE=false", "NODE_EXTRA_CA_CERTS=" + certPath, "SSL_CERT_FILE=" + certPath}
	cmd.Stdin = strings.NewReader("y\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local native device-code login failed: %v\n%s", err, output)
	}
	config, err := os.ReadFile(filepath.Join(home, ".copilot", "config.json"))
	if err != nil || !strings.Contains(string(config), "github-sentinel") {
		t.Fatalf("native device-code login did not store the dummy token: %v", err)
	}
	// Force the next process to read and use the stored token instead of cached account metadata.
	if err := os.RemoveAll(filepath.Join(home, ".cache", "copilot")); err != nil {
		t.Fatal(err)
	}
	loginComplete.Store(true)
	accountQueries.Store(0)
	return certPath
}
