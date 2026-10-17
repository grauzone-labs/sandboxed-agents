package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func connectTestRemote(t *testing.T, a, b *authServiceFixture, token string) instanceInfo {
	t.Helper()
	var info instanceInfo
	if err := a.owner.jsonCall("/api/connections", "local-fake", connectionRequest{Action: "connect", Label: "Second backend", URL: b.server.URL, Token: token}, &info); err != nil {
		t.Fatal(err)
	}
	if info.Status != "connected" || info.ID == "local-fake" || info.ServiceID != b.fleet.service.ID {
		t.Fatal("must register the actual independent backend")
	}
	return info
}
func reconnectTestRemote(t *testing.T, a *authServiceFixture, id, token string) {
	t.Helper()
	var info instanceInfo
	if err := a.owner.jsonCall("/api/connections", "local-fake", connectionRequest{Action: "reconnect", ID: id, Token: token}, &info); err != nil {
		t.Fatal(err)
	}
	if info.ID != id || info.Status != "connected" {
		t.Fatal("reconnect must preserve connection identity")
	}
}
func snapshotTest(t *testing.T, c *serviceClient, id string) instanceState {
	t.Helper()
	state, err := c.snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// TestRemoteEnvironmentIsolation proves all reads, mutations, forms, reviews and terminals reach B while A retains its own engine through HTTP/WS seams.
func TestRemoteEnvironmentIsolation(t *testing.T) {
	a, b := authService(t), authService(t)
	initial := snapshotTest(t, a.owner, "local-fake")
	if len(initial.Instances) != 1 || len(a.fleet.hosts) != 1 || initial.Origin != nil {
		t.Fatal("initial fleet must contain only this process's local engine")
	}
	token := issueTestToken(t, b.owner, "Connection from A", 3600)
	info := connectTestRemote(t, a, b, token.Secret)
	remote := snapshotTest(t, a.owner, info.ID)
	if remote.Service.ID != a.fleet.service.ID || remote.Origin == nil || remote.Origin.ID != b.fleet.service.ID || remote.Instance.ID != info.ID || len(remote.Sandboxes) != 5 || len(remote.Instances) != 2 {
		t.Fatal("snapshot must distinguish broker, connection and actual origin")
	}
	code, data, _ := authRequest(t, a.server.URL, "/api/connections", a.fleet.ownerCredential, "GET", nil, "")
	if code != 200 || bytes.Contains(data, []byte(token.Secret)) || bytes.Contains(data, []byte(b.fleet.ownerCredential)) {
		t.Fatal("connection metadata must not disclose outbound credentials")
	}
	_, err := a.owner.command(info.ID, request{Action: "start", Name: "release-check"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshotTest(t, b.owner, "local-fake").findSandbox("release-check").Status != "running" || snapshotTest(t, a.owner, "local-fake").findSandbox("release-check").Status != "stopped" {
		t.Fatal("same-name sandbox actions must be scoped to the selected connection")
	}
	create := request{Action: "create", Name: "only-on-b", ImageMode: "existing", ImageID: "img-base", Agents: []string{"codex"}, Start: true}
	if _, err = a.owner.review(info.ID, create); err != nil {
		t.Fatal(err)
	}
	if snapshotTest(t, b.owner, "local-fake").findSandbox("only-on-b") != nil {
		t.Fatal("typed review cannot create a remote sandbox")
	}
	if _, err = a.owner.command(info.ID, create); err != nil {
		t.Fatal(err)
	}
	if snapshotTest(t, b.owner, "local-fake").findSandbox("only-on-b") == nil || snapshotTest(t, a.owner, "local-fake").findSandbox("only-on-b") != nil {
		t.Fatal("remote creation must not install inventory in A")
	}
	code, _, _ = authRequest(t, a.server.URL, "/api/form?instance="+info.ID+"&action=stop&name=release-check", a.fleet.ownerCredential, "GET", nil, "")
	if code != 200 {
		t.Fatal("remote form must be provided by B")
	}
	browserStop := map[string]any{"action": "stop", "name": "release-check"}
	code, _, _ = authRequest(t, a.server.URL, "/api/review?instance="+info.ID, a.fleet.ownerCredential, "POST", browserStop, "")
	if code != 200 || snapshotTest(t, b.owner, "local-fake").findSandbox("release-check").Status != "running" {
		t.Fatal("browser remote review must not mutate B")
	}
	code, _, _ = authRequest(t, a.server.URL, "/api/command?instance="+info.ID, a.fleet.ownerCredential, "POST", browserStop, "")
	if code != 200 || snapshotTest(t, b.owner, "local-fake").findSandbox("release-check").Status != "stopped" {
		t.Fatal("browser command must act on B")
	}
	conn, _, err := authWS(t, a.server.URL, info.ID, a.fleet.ownerCredential, "", "")
	if err != nil {
		t.Fatal("must proxy B terminal")
	}
	defer conn.Close()
	terminalInitial(t, conn)
	if err = conn.WriteJSON(map[string]string{"type": "input", "data": "only-b-history\r"}); err != nil {
		t.Fatal("remote input failed")
	}
	var event terminalEvent
	if conn.ReadJSON(&event) != nil || !bytes.Contains([]byte(event.Data), []byte("only-b-history")) {
		t.Fatal("remote terminal must process input")
	}
	local, _, err := authWS(t, a.server.URL, "local-fake", a.fleet.ownerCredential, "", "")
	if err != nil {
		t.Fatal("must open A local terminal")
	}
	defer local.Close()
	_ = local.SetReadDeadline(time.Now().Add(time.Second))
	if local.ReadJSON(&event) != nil || bytes.Contains([]byte(event.Data), []byte("only-b-history")) {
		t.Fatal("identically named local session must have its own history")
	}
	if err = a.owner.jsonCall("/api/connections", "local-fake", connectionRequest{Action: "disconnect", ID: info.ID}, nil); err != nil {
		t.Fatal(err)
	}
	terminalClosed(t, conn)
	if _, err = a.owner.command(info.ID, request{Action: "start", Name: "release-check"}); err == nil {
		t.Fatal("disconnected remote must reject mutations")
	}
	remote = snapshotTest(t, a.owner, info.ID)
	if remote.Instance.Status != "disconnected" || remote.Service.ID != a.fleet.service.ID || remote.findSandbox("only-on-b") == nil {
		t.Fatal("disconnected snapshot must retain explicitly read-only cache and A availability")
	}
	if snapshotTest(t, b.owner, "local-fake").findSandbox("api-refactor").Sessions != 1 {
		t.Fatal("disconnect must retain B agent session")
	}
	replacement := issueTestToken(t, b.owner, "Replacement", 3600)
	reconnectTestRemote(t, a, info.ID, replacement.Secret)
	if snapshotTest(t, a.owner, info.ID).findSandbox("only-on-b") == nil {
		t.Fatal("reconnect must preserve B state")
	}
	conn2, _, err := authWS(t, a.server.URL, info.ID, a.fleet.ownerCredential, "", "")
	if err != nil {
		t.Fatal("reconnected terminal failed")
	}
	defer conn2.Close()
	terminalInitial(t, conn2)
	revokeTestToken(t, b.owner, replacement.Token.ID)
	terminalClosed(t, conn2)
	remote = snapshotTest(t, a.owner, info.ID)
	if remote.Instance.Status != "disconnected" || remote.Service.ID != a.fleet.service.ID {
		t.Fatal("B revocation must leave A alive with disconnected remote status")
	}
	code, _, _ = authRequest(t, a.server.URL, "/api/action?instance="+info.ID, a.fleet.ownerCredential, "POST", request{Action: "start", Name: "release-check"}, "")
	if code != 409 {
		t.Fatalf("B auth failure must remain remote-specific, got %d", code)
	}
	issueTestToken(t, a.owner, "Still managing A", 3600)
	if len(a.fleet.hosts) != 1 || len(snapshotTest(t, a.owner, "local-fake").Sandboxes) != 5 {
		t.Fatal("A engine must never be replaced by B inventory")
	}
}

// TestRemoteConnectionValidation proves invalid, duplicate, self and recursive targets cannot gain a fallback or disclose remote chains through the HTTP API seam.
func TestRemoteConnectionValidation(t *testing.T) {
	a, b, c := authService(t), authService(t), authService(t)
	cToken := issueTestToken(t, c.owner, "C", 3600)
	nested := connectTestRemote(t, b, c, cToken.Secret)
	token := issueTestToken(t, b.owner, "B", 3600)
	info := connectTestRemote(t, a, b, token.Secret)
	for _, input := range []connectionRequest{
		{Action: "connect", Label: "Self", URL: a.server.URL, Token: a.fleet.ownerCredential},
		{Action: "connect", Label: "Duplicate", URL: b.server.URL, Token: token.Secret},
		{Action: "connect", Label: "Wrong token", URL: c.server.URL, Token: opaqueSecret()},
		{Action: "connect", Label: "No token", URL: c.server.URL},
		{Action: "connect", Label: "Unsafe transport", URL: "http://192.0.2.10:4317", Token: token.Secret},
		{Action: "connect", Label: "Path", URL: b.server.URL + "/api/state", Token: token.Secret},
		{Action: "connect", Label: "URL credential", URL: "https://user:password@example.test", Token: token.Secret},
		{Action: "connect", Label: "Query", URL: b.server.URL + "?token=redacted", Token: token.Secret},
		{Action: "reconnect", ID: "missing", Token: token.Secret}, {Action: "disconnect", ID: "missing"},
		{Action: "reconnect", ID: info.ID},
	} {
		if err := a.owner.jsonCall("/api/connections", "local-fake", input, nil); err == nil {
			t.Errorf("%s / %s must be rejected", input.Action, input.Label)
		}
	}
	if len(snapshotTest(t, a.owner, info.ID).Instances) != 2 {
		t.Fatal("B's remote connections must never be exposed as A's selectable inventory")
	}
	for _, path := range []string{"/api/state?instance=missing", "/api/action?instance=missing", "/api/state?instance=" + nested.ID} {
		method := "GET"
		var input any
		if path == "/api/action?instance=missing" {
			method = "POST"
			input = request{Action: "reset"}
		}
		code, _, _ := authRequest(t, a.server.URL, path, a.fleet.ownerCredential, method, input, "")
		if code != 404 {
			t.Fatal("unknown connection must not select the local engine")
		}
	}
	req, _ := http.NewRequest("GET", b.server.URL+"/api/state?instance="+nested.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token.Secret)
	req.Header.Set("X-Prototype-Local-Only", "1")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("local-only proxy must reject a recursive target")
	}
	if _, err := newServiceClient("https://example.test", token.Secret); err != nil {
		t.Fatal("verified HTTPS must be supported")
	}
}

// TestRemoteUnavailableKeepsLocalAdministration proves B network failure neither blocks local snapshots nor prevents A administration through the HTTP seam.
func TestRemoteUnavailableKeepsLocalAdministration(t *testing.T) {
	a, b := authService(t), authService(t)
	token := issueTestToken(t, b.owner, "B", 3600)
	info := connectTestRemote(t, a, b, token.Secret)
	b.server.Close()
	remote := snapshotTest(t, a.owner, info.ID)
	if remote.Instance.Status != "offline" || remote.Instance.Error == "" || remote.Origin.ID != b.fleet.service.ID {
		t.Fatal("failed backend must be explicit and cache must retain origin")
	}
	if _, err := a.owner.command(info.ID, request{Action: "start", Name: "release-check"}); err == nil {
		t.Fatal("offline mutations must be rejected")
	}
	if snapshotTest(t, a.owner, "local-fake").Instance.Status != "connected" {
		t.Fatal("local backend remains connected")
	}
	issueTestToken(t, a.owner, "A available", 3600)
	if err := a.owner.jsonCall("/api/connections", "local-fake", connectionRequest{Action: "disconnect", ID: info.ID}, nil); err != nil {
		t.Fatal("offline connection can still be disconnected")
	}
}

// TestRemoteSnapshotUsesCurrentConnectionGeneration proves reconnect cannot pair an old in-flight cache with new metadata and local reads avoid remote I/O through real HTTP servers.
func TestRemoteSnapshotUsesCurrentConnectionGeneration(t *testing.T) {
	a := authService(t)
	b := newFakeFleet(newFakeHost(0))
	gate, release := make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	handler := b.handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/state" && block.Swap(false) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			close(gate)
			<-release
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer b.closeConnections()
	bc, _ := newServiceClient(server.URL, b.ownerCredential)
	defer bc.close()
	token := issueTestToken(t, bc, "Original", 3600)
	replacement := issueTestToken(t, bc, "Replacement", 3600)
	var info instanceInfo
	if err := a.owner.jsonCall("/api/connections", "local-fake", connectionRequest{Action: "connect", Label: "B", URL: server.URL, Token: token.Secret}, &info); err != nil {
		t.Fatal(err)
	}
	block.Store(true)
	result := make(chan instanceState, 1)
	fail := make(chan error, 1)
	go func() {
		state, err := a.owner.snapshot(info.ID)
		if err != nil {
			fail <- err
		} else {
			result <- state
		}
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	select {
	case <-gate:
	case <-time.After(2 * time.Second):
		t.Fatal("old remote request did not reach the controlled network boundary")
	}
	local := make(chan error, 1)
	go func() { _, err := a.owner.snapshot("local-fake"); local <- err }()
	select {
	case err := <-local:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a local snapshot must not wait on an unavailable remote or fleet mutex")
	}
	if _, err := bc.command("local-fake", request{Action: "start", Name: "release-check"}); err != nil {
		t.Fatal(err)
	}
	reconnectTestRemote(t, a, info.ID, replacement.Secret)
	close(release)
	released = true
	select {
	case state := <-result:
		if state.findSandbox("release-check").Status != "running" || state.Instance.Status != "connected" {
			t.Fatal("obsolete request must compose only the current connection generation")
		}
	case <-fail:
		t.Fatal("A must still return its selected remote status after reconnect")
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot must complete after obsolete client is canceled")
	}
	// The reconnect remains intact even after the obsolete handler finishes.
	data, _ := json.Marshal(snapshotTest(t, a.owner, info.ID))
	if bytes.Contains(data, []byte(token.Secret)) || bytes.Contains(data, []byte(replacement.Secret)) {
		t.Fatal("generation changes cannot expose either credential")
	}
}
