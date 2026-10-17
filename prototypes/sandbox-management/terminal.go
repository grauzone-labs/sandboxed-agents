package main

// THROWAWAY: a persistent demo agent with real WebSocket terminal I/O.
// Input is interpreted here; no shell, SSH process or model provider is called.
import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

type terminalEvent struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
}

type demoTerminal struct {
	mu                        sync.Mutex
	instance, name, id, agent string
	history                   string
	line                      []rune
	escape                    bool
	closed                    bool
	clients                   map[chan terminalEvent]bool
}

type terminalHub struct {
	mu       sync.Mutex
	sessions map[string]*demoTerminal
}

func newTerminalHub() *terminalHub                 { return &terminalHub{sessions: map[string]*demoTerminal{}} }
func terminalKey(instance, name, id string) string { return instance + "\x00" + name + "\x00" + id }

func (h *terminalHub) get(instance string, host *fakeHost, name, id string) (*demoTerminal, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	host.mu.Lock()
	defer host.mu.Unlock()
	b := host.state.findSandbox(name)
	if b == nil || b.Status != "running" {
		return nil, fmt.Errorf("Sandbox is not running on this instance")
	}
	if host.state.Busy[name] != "" {
		return nil, fmt.Errorf("An action is running for this sandbox; retry after it finishes")
	}
	active := sessionsForSandbox(*b)
	idx := slices.IndexFunc(active, func(s agentSession) bool { return s.ID == id })
	if idx < 0 {
		return nil, fmt.Errorf("Session is not open on this instance and sandbox")
	}
	key := terminalKey(instance, name, id)
	if existing := h.sessions[key]; existing != nil {
		return existing, nil
	}
	t := &demoTerminal{instance: instance, name: name, id: id, agent: active[idx].Agent, clients: map[chan terminalEvent]bool{}}
	t.history = fmt.Sprintf("\x1b[36mSANDBOXED / %s / %s / %s\x1b[0m\r\nSession %s\r\n\r\nInteractive demo agent. No shell or model provider is connected.\r\nTry /help, /status, or type a request. Detach keeps this session and its history.\r\n\r\n%s> ", instance, name, t.agent, id, t.agent)
	h.sessions[key] = t
	return t, nil
}

func (t *demoTerminal) syncAttached(host *fakeHost) {
	// Caller holds t.mu. The host never calls back into a terminal under its lock.
	host.mu.Lock()
	defer host.mu.Unlock()
	b := host.state.findSandbox(t.name)
	if b == nil {
		return
	}
	active := sessionsForSandbox(*b)
	for idx := range active {
		if active[idx].ID == t.id {
			active[idx].Attached = len(t.clients) > 0
			b.OpenSessions = active
			return
		}
	}
}

func (t *demoTerminal) subscribe(host *fakeHost) (chan terminalEvent, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, fmt.Errorf("Session has ended")
	}
	out := make(chan terminalEvent, 64)
	out <- terminalEvent{"output", t.history}
	t.clients[out] = true
	t.syncAttached(host)
	return out, nil
}

func (t *demoTerminal) unsubscribe(out chan terminalEvent, host *fakeHost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.clients[out] {
		delete(t.clients, out)
		close(out)
	}
	if !t.closed {
		t.syncAttached(host)
	}
}

func (t *demoTerminal) emit(data string) {
	t.history += data
	if len(t.history) > 65536 {
		t.history = t.history[len(t.history)-65536:]
		for len(t.history) > 0 && !utf8.RuneStart(t.history[0]) {
			t.history = t.history[1:]
		}
	}
	for out := range t.clients {
		select {
		case out <- terminalEvent{"output", data}:
		default:
			delete(t.clients, out)
			close(out)
		}
	}
}

func (t *demoTerminal) input(data string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	var output strings.Builder
	for _, r := range data {
		if t.escape {
			if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '~' {
				t.escape = false
			}
			continue
		}
		switch r {
		case '\x1b':
			t.escape = true
		case '\r', '\n':
			line := string(t.line)
			t.line = nil
			output.WriteString("\r\n" + t.respond(line) + "\r\n" + t.agent + "> ")
		case '\x7f', '\b':
			if len(t.line) > 0 {
				t.line = t.line[:len(t.line)-1]
				output.WriteString("\b \b")
			}
		case '\x03':
			t.line = nil
			output.WriteString("^C\r\n" + t.agent + "> ")
		case '\x0c':
			output.WriteString("\x1b[2J\x1b[H" + t.agent + "> " + string(t.line))
		default:
			if r >= ' ' && len(t.line) < 2048 {
				t.line = append(t.line, r)
				output.WriteRune(r)
			}
		}
	}
	if output.Len() > 0 {
		t.emit(output.String())
	}
}

func (t *demoTerminal) respond(line string) string {
	switch strings.TrimSpace(line) {
	case "":
		return ""
	case "/help":
		return "/help   Show demo commands\r\n/status Show session and instance\r\n/clear  Clear the terminal\r\nAny other text receives a simulated agent response."
	case "/status":
		return fmt.Sprintf("Instance: %s\r\nSandbox: %s\r\nAgent: %s\r\nSession: %s\r\nState: running (simulated)", t.instance, t.name, t.agent, t.id)
	case "/clear":
		return "\x1b[2J\x1b[H"
	default:
		return fmt.Sprintf("\x1b[32m%s / simulated response\x1b[0m\r\nReceived: %s\r\nI would inspect the workspace, propose a change and run checks.\r\nThis demo leaves all files untouched.", t.agent, strings.TrimSpace(line))
	}
}

func (t *demoTerminal) end(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	for out := range t.clients {
		select {
		case out <- terminalEvent{"ended", reason}:
		default:
		}
		close(out)
		delete(t.clients, out)
	}
}

func (h *terminalHub) reconcile(instance string, host *fakeHost) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := host.snapshot()
	for key, t := range h.sessions {
		if t.instance != instance {
			continue
		}
		b := s.findSandbox(t.name)
		if b != nil && b.Status == "running" && slices.ContainsFunc(b.OpenSessions, func(session agentSession) bool { return session.ID == t.id }) {
			continue
		}
		t.end("The agent session has ended. Start a new session to continue.")
		delete(h.sessions, key)
	}
}

func (h *terminalHub) reset(instance string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, t := range h.sessions {
		if t.instance == instance {
			t.end("The fake instance was reset.")
			delete(h.sessions, key)
		}
	}
}

func (fleet *fakeFleet) serveTerminal(w http.ResponseWriter, r *http.Request) {
	instance := r.URL.Query().Get("instance")
	if instance == "" {
		instance = "local-fake"
	}
	host := fleet.hosts[instance]
	if host == nil {
		http.Error(w, "Unknown instance", http.StatusNotFound)
		return
	}
	t, err := fleet.terminals.get(instance, host, r.URL.Query().Get("sandbox"), r.URL.Query().Get("session"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Gorilla's default origin check permits same-origin browser connections only.
	upgrader := terminalUpgrader(r)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	g := r.Context().Value(grantKey{}).(*authGrant)
	go func() {
		select {
		case <-g.done:
		case <-done:
		}
		_ = conn.Close()
	}()
	out, err := t.subscribe(host)
	if err != nil {
		_ = conn.WriteJSON(terminalEvent{"ended", err.Error()})
		return
	}
	defer t.unsubscribe(out, host)
	go func() {
		defer conn.Close()
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case event, ok := <-out:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if conn.WriteJSON(event) != nil {
					return
				}
			case <-done:
				return
			case <-g.done:
				return
			case <-ping.C:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if conn.WriteMessage(websocket.PingMessage, nil) != nil {
					return
				}
			}
		}
	}()
	conn.SetReadLimit(8192)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	for {
		var message struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if conn.ReadJSON(&message) != nil {
			break
		}
		switch message.Type {
		case "input":
			t.input(message.Data)
		case "resize":
			if message.Cols < 2 || message.Cols > 400 || message.Rows < 2 || message.Rows > 200 {
				return
			}
		case "detach":
			return
		default:
			return
		}
	}
}
