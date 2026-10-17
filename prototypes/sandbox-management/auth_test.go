package main

import (
	"bytes"
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestManagementRequiresAuthentication proves loopback is not an owner identity at the HTTP API seam.
func TestManagementRequiresAuthentication(t *testing.T) {
	server := httptest.NewServer(newFakeFleet(newFakeHost(0)).handler())
	defer server.Close()
	for _, path := range []string{"/api/state", "/api/form?action=reset", "/api/terminal?sandbox=api-refactor&session=api-refactor-session-01"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Errorf("%s: got %d, want 401", path, response.StatusCode)
		}
	}
}

type authServiceFixture struct {
	fleet  *fakeFleet
	server *httptest.Server
	owner  *serviceClient
}

func authService(t *testing.T) *authServiceFixture {
	t.Helper()
	f := newFakeFleet(newFakeHost(0))
	server := httptest.NewServer(f.handler())
	c, err := newServiceClient(server.URL, f.ownerCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.close(); f.closeConnections(); server.Close() })
	return &authServiceFixture{f, server, c}
}

type createdToken struct {
	Token  tokenInfo `json:"token"`
	Secret string    `json:"secret"`
}

func issueTestToken(t *testing.T, c *serviceClient, name string, seconds int) createdToken {
	t.Helper()
	var result createdToken
	if err := c.jsonCall("/api/tokens", "local-fake", map[string]any{"action": "create", "name": name, "expiresInSeconds": seconds}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Secret) != 64 || result.Token.ID == "" || result.Token.Status != "active" {
		t.Fatal("creation must return a 32-byte secret and active metadata")
	}
	return result
}
func authRequest(t *testing.T, service, path, token, method string, input any, origin string) (int, []byte, http.Header) {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal("invalid fixture")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, service+path, body)
	if err != nil {
		t.Fatal("invalid fixture request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("fixture request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal("fixture response failed")
	}
	return res.StatusCode, data, res.Header
}
func authWS(t *testing.T, service, instance, token, protocol, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	q := url.Values{"instance": {instance}, "sandbox": {"api-refactor"}, "session": {"api-refactor-session-01"}}
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	if protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", protocol)
	}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(service, "http")+"/api/terminal?"+q.Encode(), headers)
}
func terminalInitial(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event terminalEvent
	if conn.ReadJSON(&event) != nil || event.Type != "output" {
		t.Fatal("expected initial terminal output")
	}
}
func terminalClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event terminalEvent
	if conn.ReadJSON(&event) == nil {
		t.Fatal("stream must close after credential invalidation or disconnect")
	}
}
func revokeTestToken(t *testing.T, c *serviceClient, id string) {
	t.Helper()
	if err := c.jsonCall("/api/tokens", "local-fake", map[string]string{"action": "revoke", "id": id}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestIssuedTokenLifecycle proves one-time creation output, full access and revocation through real HTTP and WebSocket seams.
func TestIssuedTokenLifecycle(t *testing.T) {
	s := authService(t)
	issued := issueTestToken(t, s.owner, "Remote workstation", 3600)
	credential, err := newServiceClient(s.server.URL, issued.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.close()
	for _, path := range []string{"/api/tokens", "/api/state", "/api/connections"} {
		code, data, _ := authRequest(t, s.server.URL, path, issued.Secret, "GET", nil, "")
		if code != 200 || bytes.Contains(data, []byte(issued.Secret)) || bytes.Contains(data, []byte(s.fleet.ownerCredential)) || bytes.Contains(data, []byte(`"hash"`)) {
			t.Fatal("metadata must contain no bearer credential or stored hash")
		}
	}
	// The single privilege level intentionally includes token creation and revocation.
	secondary := issueTestToken(t, credential, "Created by issued token", 3600)
	revokeTestToken(t, credential, secondary.Token.ID)
	conn, _, err := authWS(t, s.server.URL, "local-fake", issued.Secret, "", "")
	if err != nil {
		t.Fatal("valid token must open WS")
	}
	defer conn.Close()
	terminalInitial(t, conn)
	revokeTestToken(t, s.owner, issued.Token.ID)
	terminalClosed(t, conn)
	for _, token := range []string{"", opaqueSecret(), issued.Secret} {
		for _, endpoint := range []struct{ path, method string }{
			{"/api/state", "GET"}, {"/api/form?action=reset", "GET"}, {"/api/tokens", "GET"}, {"/api/connections", "GET"},
			{"/api/tokens", "POST"}, {"/api/connections", "POST"}, {"/api/action", "POST"}, {"/api/action-review", "POST"}, {"/api/command", "POST"}, {"/api/review", "POST"}, {"/api/terminal-ticket", "POST"},
		} {
			code, _, _ := authRequest(t, s.server.URL, endpoint.path, token, endpoint.method, map[string]string{}, "")
			if code != 401 {
				t.Errorf("%s %s must reject missing/invalid/revoked auth: %d", endpoint.method, endpoint.path, code)
			}
		}
		ws, res, e := authWS(t, s.server.URL, "local-fake", token, "", "")
		if ws != nil {
			ws.Close()
		}
		if res != nil {
			res.Body.Close()
		}
		if e == nil || res == nil || res.StatusCode != 401 {
			t.Fatal("WS upgrade must require valid auth")
		}
	}
	var metadata []tokenInfo
	if err := s.owner.jsonCall("/api/tokens", "local-fake", nil, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || metadata[0].Status != "revoked" || metadata[1].Status != "revoked" {
		t.Fatal("list must retain revoked metadata without owner token")
	}
	code, page, headers := authRequest(t, s.server.URL, "/", "", "GET", nil, "")
	if code != 200 || headers.Get("Set-Cookie") != "" || bytes.Contains(page, []byte(s.fleet.ownerCredential)) || !bytes.Contains(page, []byte(`id="login-gate"`)) {
		t.Fatal("public HTML must offer a login gate without owner bootstrap disclosure or cookies")
	}
}

// TestTokenExpiryClosesExistingStreams proves expiry invalidates active streams and later requests at HTTP/WS seams.
func TestTokenExpiryClosesExistingStreams(t *testing.T) {
	s := authService(t)
	issued := issueTestToken(t, s.owner, "Short lived", 1)
	conn, _, err := authWS(t, s.server.URL, "local-fake", issued.Secret, "", "")
	if err != nil {
		t.Fatal("short token should initially open WS")
	}
	defer conn.Close()
	terminalInitial(t, conn)
	terminalClosed(t, conn)
	code, _, _ := authRequest(t, s.server.URL, "/api/state", issued.Secret, "GET", nil, "")
	if code != 401 {
		t.Fatal("expired token must return 401")
	}
	var metadata []tokenInfo
	if s.owner.jsonCall("/api/tokens", "local-fake", nil, &metadata) != nil || len(metadata) != 1 || metadata[0].Status != "expired" {
		t.Fatal("expired status must be visible to remaining administrators")
	}
}

// TestBrowserTicketsAndPortIsolation proves origin checks and one-use ticket authentication without cross-port cookie credentials through real servers.
func TestBrowserTicketsAndPortIsolation(t *testing.T) {
	a, b := authService(t), authService(t)
	issued := issueTestToken(t, a.owner, "Browser", 3600)
	code, _, headers := authRequest(t, a.server.URL, "/api/state", issued.Secret, "GET", nil, a.server.URL)
	if code != 200 || headers.Get("Set-Cookie") != "" {
		t.Fatal("same-origin bearer login must not create cookies")
	}
	code, _, _ = authRequest(t, b.server.URL, "/api/state", issued.Secret, "GET", nil, b.server.URL)
	if code != 401 {
		t.Fatal("same-host different-port service must reject another service's bearer")
	}
	for _, endpoint := range []struct{ path, method string }{
		{"/api/state", "GET"}, {"/api/action", "POST"}, {"/api/tokens", "POST"}, {"/api/connections", "POST"}, {"/api/terminal-ticket", "POST"},
	} {
		code, _, _ = authRequest(t, a.server.URL, endpoint.path, issued.Secret, endpoint.method, map[string]string{}, b.server.URL)
		if code != 403 {
			t.Fatal("foreign port origin must not read or mutate APIs")
		}
	}
	ticket := func() string {
		t.Helper()
		code, data, _ := authRequest(t, a.server.URL, "/api/terminal-ticket", issued.Secret, "POST", map[string]string{"sandbox": "api-refactor", "session": "api-refactor-session-01"}, a.server.URL)
		var result struct {
			Ticket string `json:"ticket"`
		}
		if code != 200 || json.Unmarshal(data, &result) != nil || len(result.Ticket) != 64 {
			t.Fatal("authenticated browser must receive a short-lived terminal ticket")
		}
		return result.Ticket
	}
	protocol := "sandbox-ticket." + ticket()
	conn, _, err := authWS(t, a.server.URL, "local-fake", "", protocol, a.server.URL)
	if err != nil {
		t.Fatal("one-use ticket must authenticate WS in protocol header")
	}
	defer conn.Close()
	terminalInitial(t, conn)
	for _, service := range []string{a.server.URL, b.server.URL} {
		ws, res, e := authWS(t, service, "local-fake", "", protocol, service)
		if ws != nil {
			ws.Close()
		}
		if res != nil {
			res.Body.Close()
		}
		if e == nil || res == nil || res.StatusCode != 401 {
			t.Fatal("used ticket must not replay or work on another service")
		}
	}
	unused := "sandbox-ticket." + ticket()
	revokeTestToken(t, a.owner, issued.Token.ID)
	terminalClosed(t, conn)
	ws, res, e := authWS(t, a.server.URL, "local-fake", "", unused, a.server.URL)
	if ws != nil {
		ws.Close()
	}
	if res != nil {
		res.Body.Close()
	}
	if e == nil || res == nil || res.StatusCode != 401 {
		t.Fatal("revoking the issuing token must invalidate unused tickets")
	}
	fresh := issueTestToken(t, a.owner, "Fresh", 3600)
	ws, res, e = authWS(t, a.server.URL, "local-fake", fresh.Secret, "", b.server.URL)
	if ws != nil {
		ws.Close()
	}
	if res != nil {
		res.Body.Close()
	}
	if e == nil || res == nil || res.StatusCode != 403 {
		t.Fatal("WS must reject foreign port origin even with a valid bearer")
	}
	// Ambient credentials are deliberately unsupported, even on loopback.
	req, _ := http.NewRequest("GET", a.server.URL+"/api/state", nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: fresh.Secret})
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("cookies cannot authenticate management APIs")
	}
}
