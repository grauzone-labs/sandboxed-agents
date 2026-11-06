package manager_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
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

// TestCopilotProxyNativeClient proves the real catalog Copilot sends streaming tool-enabled requests using the configured provider without leaking GitHub credentials through RunWithEffects and both HTTP wire APIs.
func TestCopilotProxyNativeClient(t *testing.T) {
	binary := os.Getenv("SANDBOXED_AGENTS_TEST_COPILOT")
	if binary == "" {
		t.Skip("set SANDBOXED_AGENTS_TEST_COPILOT to Copilot 1.0.106 for the native contract test")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range interfaces {
		if device.Flags&net.FlagLoopback == 0 {
			t.Fatal("native contract test requires loopback-only networking; run python3 scripts/test-copilot-proxy.py")
		}
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
		{"completions", "cached-account"}, {"responses", "cached-account"},
	} {
		wire := scenario.wire
		t.Run(fmt.Sprintf("%s/auth=%s", wire, scenario.auth), func(t *testing.T) {
			var calls, accountQueries, catalogQueries atomic.Int64
			metadata, metadataCA := copilotMetadataServer(t, &accountQueries, &catalogQueries)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				// Terminate only the two observed GitHub hosts locally, then inspect the encrypted HTTP request.
				if r.Method == "CONNECT" && (r.Host == "api.github.com:443" || r.Host == "api.githubcopilot.com:443") {
					copilotMetadataTunnel(t, w, metadata.Listener.Addr().String())
					return
				}
				if r.Method != http.MethodPost || r.Host != server.Listener.Addr().String() || r.URL.Path != expected || r.URL.RawQuery != "" {
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
				calls.Add(1)
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
			if scenario.auth == "device-code" || scenario.auth == "cached-account" {
				certPath = copilotDeviceCodeLogin(t, binary, home, &storedAccountQueries, scenario.auth == "cached-account")
			}
			var trust []byte
			if certPath != "" {
				trust, err = os.ReadFile(certPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			certPath = filepath.Join(home, "native-ca.pem")
			if err := os.WriteFile(certPath, append(trust, metadataCA...), 0600); err != nil {
				t.Fatal(err)
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
			if calls.Load() != 1 || !strings.Contains(string(output), "OK") {
				t.Fatalf("no successful inference: calls=%d output=%s", calls.Load(), output)
			}
			if (accountQueries.Load()+storedAccountQueries.Load() > 0) != (scenario.auth != "none") {
				t.Fatalf("GitHub credential use not observed as expected: auth=%s account queries=%d stored account queries=%d", scenario.auth, accountQueries.Load(), storedAccountQueries.Load())
			}
			if (catalogQueries.Load() > 0) != (scenario.auth == "cached-account") {
				t.Fatalf("catalog discovery not observed as expected: auth=%s catalog queries=%d", scenario.auth, catalogQueries.Load())
			}
			t.Logf("Copilot completed %s with %d streaming tool-enabled request(s), %d blocked GitHub account queries, %d stored-credential account queries, %d catalog GETs", wire, calls.Load(), accountQueries.Load(), storedAccountQueries.Load(), catalogQueries.Load())
		})
	}
}

// copilotDeviceCodeLogin returns the local identity CA path after native device-code login, resets the credential-use counter, and optionally retains successful account metadata.
func copilotDeviceCodeLogin(t *testing.T, binary, home string, accountQueries *atomic.Int64, cached bool) string {
	t.Helper()
	var loginComplete atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(fmt.Sprint(r.Header)+r.URL.String()+string(body), "local-test-secret") {
			t.Error("provider key leaked to the identity fixture")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			if r.Method != http.MethodPost {
				t.Error("device-code request must be POST")
			}
			io.WriteString(w, `{"device_code":"local-device","user_code":"LOCAL","verification_uri":"https://127.0.0.1/local-only","expires_in":900,"interval":1}`)
		case "/login/oauth/access_token":
			if r.Method != http.MethodPost {
				t.Error("token request must be POST")
			}
			io.WriteString(w, `{"access_token":"github-sentinel","token_type":"bearer","scope":"read:user"}`)
		case "/copilot_internal/user", "/copilot_internal/managed_settings", "/copilot_internal/team_customizations":
			if r.Method != http.MethodGet {
				t.Error("identity metadata request must be GET")
			}
			if !strings.Contains(r.Header.Get("Authorization"), "github-sentinel") {
				t.Error("native client did not send its stored credential to the identity fixture")
			}
			accountQueries.Add(1)
			if loginComplete.Load() && !cached {
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
	if !cached {
		if err := os.RemoveAll(filepath.Join(home, ".cache", "copilot")); err != nil {
			t.Fatal(err)
		}
	}
	loginComplete.Store(true)
	accountQueries.Store(0)
	return certPath
}

// copilotMetadataServer returns a local TLS endpoint and its CA, accepting authenticated metadata GETs only and rejecting inference or provider credentials on GitHub destinations.
func copilotMetadataServer(t *testing.T, accountQueries, catalogQueries *atomic.Int64) (*httptest.Server, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"api.github.com", "api.githubcopilot.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &certificate, &certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(fmt.Sprint(r.Header)+r.URL.String()+string(body), "local-test-secret") {
			t.Error("provider key leaked to GitHub metadata")
		}
		if !strings.Contains(r.Header.Get("Authorization"), "github-sentinel") || strings.Contains(r.URL.String()+string(body), "github-sentinel") {
			t.Error("GitHub metadata credential missing or outside Authorization")
		}
		if r.Method != http.MethodGet || len(body) != 0 || r.URL.RawQuery != "" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL)
			http.Error(w, "inference and unknown metadata forbidden", http.StatusForbidden)
			return
		}
		switch {
		case r.Host == "api.githubcopilot.com" && r.URL.Path == "/models":
			catalogQueries.Add(1)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[]}`)
		case r.Host == "api.github.com" && r.URL.Path == "/copilot_internal/user":
			accountQueries.Add(1)
			http.Error(w, "account metadata blocked", http.StatusForbidden)
		default:
			t.Errorf("unexpected GitHub destination: %s %s", r.Host, r.URL)
			http.Error(w, "blocked", http.StatusForbidden)
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// copilotMetadataTunnel forwards an allowed CONNECT to the local TLS fixture, preserving buffered bytes and closing both directions when the client disconnects.
func copilotMetadataTunnel(t *testing.T, w http.ResponseWriter, address string) {
	t.Helper()
	upstream, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Error(err)
		return
	}
	defer upstream.Close()
	client, buffered, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(45 * time.Second))
	upstream.SetDeadline(time.Now().Add(45 * time.Second))
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		t.Error(err)
		return
	}
	if err := buffered.Flush(); err != nil {
		t.Error(err)
		return
	}
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, buffered)
		upstream.Close()
		close(done)
	}()
	io.Copy(client, upstream)
	client.Close()
	<-done
}
