package main

import (
	"bytes"
	"encoding/json"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSharedServiceCommandAPI proves that a typed TUI command changes the state served to the existing web API through a real HTTP service.
func TestSharedServiceCommandAPI(t *testing.T) {
	host := newFakeHost(0)
	server := httptest.NewServer(browserHandler(host))
	defer server.Close()
	data, _ := json.Marshal(request{Action: "start", Name: "release-check"})
	response, err := http.Post(server.URL+"/api/action?instance=local-fake", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("typed command endpoint: got %d", response.StatusCode)
	}
	stateResponse, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer stateResponse.Body.Close()
	var state instanceState
	if err = json.NewDecoder(stateResponse.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state.findSandbox("release-check").Status != "running" {
		t.Fatal("web state must show the TUI command")
	}
}

func serviceUI(t *testing.T, url string) *tui {
	t.Helper()
	client, err := newServiceClient(url)
	if err != nil {
		t.Fatal(err)
	}
	m := newConnectedTUI(client, "local-fake")
	t.Cleanup(m.close)
	completeService(t, m, m.Init())
	return m
}
func completeService(t *testing.T, m *tui, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a service command")
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case msg := <-out:
		_, next := m.Update(msg)
		return next
	case <-time.After(4 * time.Second):
		t.Fatal("service command timed out")
		return nil
	}
}
func refreshClient(t *testing.T, m *tui) {
	t.Helper()
	_, cmd := m.Update(refreshMsg{})
	completeService(t, m, cmd)
}
func readServiceState(t *testing.T, url string) instanceState {
	t.Helper()
	response, err := http.Get(url + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var s instanceState
	if err = json.NewDecoder(response.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
func freeServicePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestWebAndTUIShareState proves both directions of synchronization through the real HTTP API and Bubble Tea command seam.
func TestWebAndTUIShareState(t *testing.T) {
	server := httptest.NewServer(browserHandler(newFakeHost(0)))
	defer server.Close()
	m := serviceUI(t, server.URL)
	for range 3 {
		press(m, tea.KeyDown)
	}
	completeService(t, m, typeKeys(m, "s"))
	if readServiceState(t, server.URL).findSandbox("release-check").Status != "running" {
		t.Fatal("web must see the TUI start")
	}
	payload := []byte(`{"action":"stop","name":"release-check"}`)
	response, err := http.Post(server.URL+"/api/command", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	refreshClient(t, m)
	if !strings.Contains(m.View(), "release-check / stopped") {
		t.Fatal("TUI must see the web stop on its selected sandbox")
	}
	if m.host != nil || m.fleet != nil {
		t.Fatal("a remote client must not construct a substitute fake host")
	}
}

// TestServiceToggleKeepsState proves automatic hosting, disable/enable and retained local changes through Configuration keystrokes and real HTTP requests.
func TestServiceToggleKeepsState(t *testing.T) {
	m, err := newHostedTUI(newFakeHost(0), freeServicePort(t), true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	url := m.ownedService.address()
	initial := readServiceState(t, url)
	external := serviceUI(t, url)
	typeKeys(m, "3")
	press(m, tea.KeyTab)
	completeService(t, m, press(m, tea.KeyEnter))
	if !strings.Contains(m.View(), "Enable service") {
		t.Fatal("Configuration must now offer Enable service")
	}
	if r, err := http.Get(url + "/api/state"); err == nil {
		r.Body.Close()
		t.Fatal("disabled service must close HTTP access")
	}
	refreshClient(t, external)
	if !strings.Contains(external.View(), "OFFLINE") {
		t.Fatal("other TUI must report the disconnected service")
	}
	if cmd := typeKeys(external, "x"); cmd != nil {
		t.Fatal("offline mutations must not be queued")
	}
	typeKeys(m, "n")
	press(m, tea.KeyCtrlS)
	typeKeys(m, "kept-while-disabled")
	press(m, tea.KeyCtrlS)
	press(m, tea.KeyCtrlK)
	completeService(t, m, press(m, tea.KeyCtrlS))
	press(m, tea.KeyEnter)
	typeKeys(m, "3")
	press(m, tea.KeyTab)
	completeService(t, m, press(m, tea.KeyEnter))
	state := readServiceState(t, url)
	if state.Service.ID != initial.Service.ID || state.findSandbox("kept-while-disabled") == nil {
		t.Fatal("Enable must expose the same engine and retained state")
	}
	refreshClient(t, external)
	if !strings.Contains(external.View(), "Connected") {
		t.Fatal("the second TUI must reconnect")
	}
	if state.findSandbox("api-refactor").Status != "running" {
		t.Fatal("offline client actions must never run on reconnect")
	}
}

// TestSharedServiceCreationWizard proves server-side reviews and creation through the HTTP-connected TUI wizard.
func TestSharedServiceCreationWizard(t *testing.T) {
	server := httptest.NewServer(browserHandler(newFakeHost(0)))
	defer server.Close()
	m := serviceUI(t, server.URL)
	typeKeys(m, "n")
	press(m, tea.KeyCtrlS)
	typeKeys(m, "http-wizard")
	completeService(t, m, press(m, tea.KeyCtrlS))
	completeService(t, m, press(m, tea.KeyCtrlK))
	if readServiceState(t, server.URL).findSandbox("http-wizard") != nil {
		t.Fatal("remote reviews must leave state unchanged")
	}
	completeService(t, m, press(m, tea.KeyCtrlS))
	if readServiceState(t, server.URL).findSandbox("http-wizard") == nil {
		t.Fatal("web must see the newly created sandbox")
	}
}

// TestSharedSelectionSurvivesOtherClientChanges proves identity-based selection and safe handling of deletion through API updates and TUI refreshes.
func TestSharedSelectionSurvivesOtherClientChanges(t *testing.T) {
	server := httptest.NewServer(browserHandler(newFakeHost(0)))
	defer server.Close()
	m := serviceUI(t, server.URL)
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	client, _ := newServiceClient(server.URL)
	defer client.close()
	if _, err := client.command("local-fake", request{Action: "remove", Name: "web-playground", Confirm: "web-playground"}); err != nil {
		t.Fatal(err)
	}
	refreshClient(t, m)
	if !strings.Contains(m.View(), "docs-agent / running") {
		t.Fatal("deleting a preceding row must not change the selected sandbox")
	}
	if _, err := client.command("local-fake", request{Action: "remove", Name: "docs-agent", Confirm: "docs-agent"}); err != nil {
		t.Fatal(err)
	}
	refreshClient(t, m)
	if !strings.Contains(m.View(), "No sandbox selected") {
		t.Fatal("deleted selection must not silently select another sandbox")
	}
	if cmd := typeKeys(m, "x"); cmd != nil {
		t.Fatal("a stale selection must not run an action on a different sandbox")
	}
}

// TestWebAndTUIShareTerminal proves bidirectional session input, shared history and correct attachment counts through real WebSockets and Bubble Tea.
func TestWebAndTUIShareTerminal(t *testing.T) {
	server := httptest.NewServer(browserHandler(newFakeHost(0)))
	defer server.Close()
	m := serviceUI(t, server.URL)
	typeKeys(m, "e")
	press(m, tea.KeyTab)
	wait := completeService(t, m, press(m, tea.KeyEnter))
	wait = completeService(t, m, wait)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/terminal?instance=local-fake&sandbox=api-refactor&session=api-refactor-session-01"
	web, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	_ = web.SetReadDeadline(time.Now().Add(3 * time.Second))
	var event terminalEvent
	if err = web.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if err = web.WriteJSON(map[string]string{"type": "input", "data": "/status\r"}); err != nil {
		t.Fatal(err)
	}
	wait = completeService(t, m, wait)
	if !strings.Contains(m.View(), "Instance: local-fake") {
		t.Fatal("TUI must receive the web terminal's response")
	}
	if err = web.ReadJSON(&event); err != nil {
		t.Fatal(err)
	} // own status output
	typeKeys(m, "from-tui")
	press(m, tea.KeyEnter)
	if err = web.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Data != "from-tui" {
		t.Fatalf("web must see TUI input: %q", event.Data)
	}
	if err = web.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.Data, "Received: from-tui") {
		t.Fatal("web must see the agent response to TUI input")
	}
	press(m, tea.KeyEsc)
	if !readServiceState(t, server.URL).findSandbox("api-refactor").OpenSessions[0].Attached {
		t.Fatal("web remains attached when TUI detaches")
	}
	wait = completeService(t, m, press(m, tea.KeyEnter))
	completeService(t, m, wait)
	if !strings.Contains(m.View(), "Received: from-tui") {
		t.Fatal("reattaching must replay shared service history")
	}
	press(m, tea.KeyEsc)
}
