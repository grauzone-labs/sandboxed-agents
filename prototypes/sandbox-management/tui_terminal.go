package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type tuiTerminal struct {
	name, agent, id string
	send            func(string) error
	closeStream     func()
	session         *demoTerminal
	events          chan terminalEvent
	host            *fakeHost
	history         string
	scroll          int
	ended           bool
}
type terminalOutput struct {
	terminal *tuiTerminal
	event    terminalEvent
	ok       bool
}

func waitTerminal(t *tuiTerminal) tea.Cmd {
	return func() tea.Msg { event, ok := <-t.events; return terminalOutput{t, event, ok} }
}

type attachReady struct {
	terminal *tuiTerminal
	err      error
}

func (m *tui) finishAttach(msg attachReady) tea.Cmd {
	m.pending = false
	if msg.err != nil {
		m.message(msg.err.Error(), true)
		return nil
	}
	m.terminal = msg.terminal
	m.message("Attached. Esc detaches; the session stays in the shared backend.", false)
	return waitTerminal(m.terminal)
}
func (m *tui) attach(r request) tea.Cmd {
	if m.remote != nil {
		if !m.online {
			m.message("Backend offline. Reconnect before attaching.", true)
			return nil
		}
		client, id := m.remote, m.instance
		agent := "agent"
		if b := m.state.findSandbox(r.Name); b != nil {
			for _, s := range sessionsForSandbox(*b) {
				if s.ID == r.Session {
					agent = s.Agent
				}
			}
		}
		m.pending = true
		return func() tea.Msg {
			t, err := client.terminal(id, r.Name, r.Session)
			if t != nil {
				t.agent = agent
			}
			return attachReady{t, err}
		}
	}
	t, err := m.fleet.terminals.get(m.instance, m.host, r.Name, r.Session)
	if err != nil {
		m.message(err.Error(), true)
		return nil
	}
	out, err := t.subscribe(m.host)
	if err != nil {
		m.message(err.Error(), true)
		return nil
	}
	host := m.host
	m.terminal = &tuiTerminal{session: t, events: out, host: host, name: r.Name, agent: t.agent, id: r.Session, send: func(data string) error { t.input(data); return nil }, closeStream: func() { t.unsubscribe(out, host) }}
	m.state = m.host.snapshot()
	m.message("Attached / "+r.Session+". Esc detaches; Ctrl+C interrupts the demo agent.", false)
	return waitTerminal(m.terminal)
}
func (m *tui) receiveTerminal(msg terminalOutput) tea.Cmd {
	if m.terminal != msg.terminal {
		return nil
	}
	if msg.event.Type == "disconnected" {
		m.terminal.ended = true
		m.message("Terminal connection lost. The agent session may still be running; reattach to continue.", true)
		return nil
	}
	if !msg.ok || msg.event.Type == "ended" {
		m.terminal.ended = true
		m.message("Session ended. Esc returns to the sandbox.", true)
		return nil
	}
	m.terminal.history += msg.event.Data
	if len(m.terminal.history) > 131072 {
		m.terminal.history = m.terminal.history[len(m.terminal.history)-131072:]
	}
	return waitTerminal(m.terminal)
}
func (m *tui) detachTerminal() {
	t := m.terminal
	t.closeStream()
	m.terminal = nil
	if m.remote == nil {
		m.state = m.host.snapshot()
	}
	m.message("Detached. The agent session and its history are still available.", false)
}
func (m *tui) terminalKey(msg tea.KeyMsg) tea.Cmd {
	t := m.terminal
	key := msg.String()
	switch key {
	case "esc", "ctrl+d", "ctrl+g":
		m.detachTerminal()
		return nil
	case "pgup":
		t.scroll += max(2, m.height/3)
		return nil
	case "pgdown":
		t.scroll = max(0, t.scroll-max(2, m.height/3))
		return nil
	}
	if t.ended {
		return nil
	}
	data := ""
	switch key {
	case "enter":
		data = "\r"
	case "backspace", "ctrl+h":
		data = "\x7f"
	case "ctrl+c", "ctrl+u":
		data = "\x03"
	case "ctrl+l":
		data = "\x0c"
	default:
		if msg.Type == tea.KeyRunes {
			data = cleanInput(string(msg.Runes))
		}
	}
	if data != "" {
		t.scroll = 0
		if err := t.send(data); err != nil {
			m.message(err.Error(), true)
		}
	}
	return nil
}

// terminalText interprets the demo's small output vocabulary, never arbitrary shell output.
func terminalText(raw string) string {
	if n := strings.LastIndex(raw, "\x1b[2J\x1b[H"); n >= 0 {
		raw = raw[n+7:]
	}
	text := ansi.Strip(raw)
	out := []rune{}
	for _, r := range text {
		switch r {
		case '\r':
			continue
		case '\b':
			if len(out) > 0 && out[len(out)-1] != '\n' {
				out = out[:len(out)-1]
			}
		default:
			if r == '\n' || r >= 32 {
				out = append(out, r)
			}
		}
	}
	return string(out)
}
func (m *tui) terminalView(w, h int) string {
	t := m.terminal
	text := terminalText(t.history)
	lines := strings.Split(ansi.Wrap(text, max(1, w-6), " "), "\n")
	rows := max(1, h-4)
	t.scroll = min(max(0, t.scroll), max(0, len(lines)-rows))
	end := len(lines) - t.scroll
	start := max(0, end-rows)
	content := strings.Join(lines[start:end], "\n")
	hint := "Type a request · /help /status /clear · Esc Detach"
	if t.scroll > 0 {
		hint = fmt.Sprintf("History / %d lines above the prompt · PgDn to return", t.scroll)
	}
	if t.ended {
		hint = "Session ended · Esc Return"
	}
	return panel("Session terminal / "+t.name+" / "+t.agent, content+"\n"+muted.Render(hint), w, h)
}
