package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"time"
)

type serviceTick struct{ loop uint64 }
type serviceSnapshot struct {
	state          instanceState
	err            error
	instance       string
	loop, revision uint64
	switching      bool
}
type serviceReview struct {
	state   instanceState
	err     error
	request request
	kind    string
	step    int
}

func newConnectedTUI(c *serviceClient, instance string) *tui {
	return &tui{remote: c, instance: instance, width: 120, height: 36, statusFilter: "all", connectionErr: "Connecting", state: fakeState{Busy: map[string]string{}}}
}
func (m *tui) instanceList() []instanceInfo {
	if m.remote != nil {
		return m.metadata.Instances
	}
	return m.fleet.instanceInfos()
}
func (m *tui) backendInfo() string {
	if m.remote == nil {
		if m.ownedService != nil {
			return "Background service disabled / local TUI state kept"
		}
		return "Standalone fake / private state"
	}
	if m.online {
		return "Backend connected / " + m.metadata.Service.ID
	}
	return "Backend offline / reconnecting · actions disabled"
}
func (m *tui) pollService() tea.Cmd {
	loop := m.pollLoop
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return serviceTick{loop} })
}
func (m *tui) fetchService(id string, switching bool) tea.Cmd {
	loop, revision, client := m.pollLoop, m.networkRevision, m.remote
	return func() tea.Msg {
		state, err := client.snapshot(id)
		return serviceSnapshot{state, err, id, loop, revision, switching}
	}
}
func (m *tui) refreshService() tea.Cmd { m.pollLoop++; return m.fetchService(m.instance, false) }
func (m *tui) applySnapshot(next instanceState, switching bool) {
	name, id := "", ""
	if b := m.box(); b != nil {
		name = b.Name
	}
	if i := m.image(); i.ID != "" {
		id = i.ID
	}
	changedService := m.metadata.Service.ID != "" && m.metadata.Service.ID != next.Service.ID
	if m.metadata.Origin != nil && next.Origin != nil && m.metadata.Origin.ID != next.Origin.ID {
		changedService = true
	}
	m.state = next.fakeState
	m.metadata = next
	m.instance = next.Instance.ID
	m.online = true
	m.connectionErr = ""
	if next.Instance.Status != "connected" && next.Instance.ID != "local-fake" {
		if switching {
			m.boxCursor, m.imageCursor = 0, 0
			m.missingBox, m.missingImage = false, false
			m.clearFocus()
		}
		m.message(next.Instance.Error, true)
		return
	}
	if switching {
		m.boxCursor, m.imageCursor = 0, 0
		m.search, m.statusFilter = "", "all"
		m.missingBox, m.missingImage = false, false
		m.clearFocus()
		return
	}
	if name != "" {
		found := false
		for n, b := range m.boxes() {
			if b.Name == name {
				m.boxCursor = n
				found = true
				break
			}
		}
		m.missingBox = !found
		if !found {
			m.clearFocus()
			m.message("The selected sandbox was removed by another client. Choose another object.", true)
		}
	}
	if id != "" {
		found := false
		for n, i := range m.images() {
			if i.ID == id {
				m.imageCursor = n
				found = true
				break
			}
		}
		m.missingImage = !found
		if !found && m.section == 1 {
			m.clearFocus()
		}
	}
	if changedService {
		m.clearFocus()
		if m.dialogMode == "image-review" {
			m.dialog = nil
			m.editDraft = nil
		}
		if m.wizard != nil && m.wizard.Step == 3 {
			m.wizard.Step = 2
			m.creationInputs()
		}
		m.message("Backend restarted. Shared fake state was reset; review pending changes again.", true)
	}
}
func (m *tui) receiveSnapshot(msg serviceSnapshot) tea.Cmd {
	if msg.loop != m.pollLoop {
		return nil
	}
	if msg.switching {
		m.pending = false
	}
	if msg.revision != m.networkRevision {
		return m.pollService()
	}
	if msg.err != nil {
		first := m.online || m.connectionErr == "Connecting"
		m.online = false
		m.connectionErr = msg.err.Error()
		if first || msg.switching {
			m.message(msg.err.Error(), true)
		}
	} else {
		wasOffline := !m.online
		m.applySnapshot(msg.state, msg.switching)
		if wasOffline {
			m.message("Connected to shared backend "+msg.state.Service.ID, false)
		}
	}
	return m.pollService()
}
func (m *tui) reviewService(r request, kind string) tea.Cmd {
	if !m.environmentOnline() {
		m.message("Backend or remote offline. Reconnect before reviewing or applying changes.", true)
		return nil
	}
	m.pending = true
	m.networkRevision++
	client, id, step := m.remote, m.instance, 0
	if m.wizard != nil {
		step = m.wizard.Step
	}
	fleet := m.fleet
	return func() tea.Msg {
		var s instanceState
		var err error
		if client != nil {
			s, err = client.review(id, r)
		} else {
			s, err = fleet.apply(id, r, true)
		}
		return serviceReview{s, err, r, kind, step}
	}
}
func (m *tui) receiveReview(msg serviceReview) tea.Cmd {
	m.pending = false
	if msg.err != nil {
		if transportError(msg.err) {
			m.online = false
			m.connectionErr = msg.err.Error()
		}
		m.message(msg.err.Error(), true)
		return nil
	}
	m.applySnapshot(msg.state, false)
	if msg.kind == "image" {
		m.enterImageReview(msg.request)
	} else if m.wizard != nil && m.wizard.Step == msg.step {
		m.wizard.Step++
		m.creationInputs()
		m.message("Review validated by the shared backend. No changes made yet.", false)
	}
	return nil
}
func (m *tui) environmentOnline() bool {
	if m.instance == "local-fake" {
		return m.remote == nil || m.online
	}
	return (m.remote == nil || m.online) && m.info().Status == "connected"
}
func (m *tui) close() {
	if m.terminal != nil {
		m.detachTerminal()
	}
	if m.remote != nil {
		m.remote.close()
	}
	if m.ownedService != nil {
		_ = m.ownedService.stop()
	}
	if m.fleet != nil {
		m.fleet.closeConnections()
	}
}
func (m *tui) connectionFailure(err error) {
	if m.remote == nil {
		m.message(err.Error(), true)
		return
	}
	if transportError(err) {
		m.online = false
		m.connectionErr = err.Error()
		m.message("Connection lost; command result unknown. State will refresh after reconnecting. "+strings.TrimPrefix(err.Error(), "Backend unavailable: "), true)
	} else {
		m.message(err.Error(), true)
	}
}
