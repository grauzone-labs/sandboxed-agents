package main

// THROWAWAY: the chosen web Control room, adapted to a keyboard-first terminal.
import (
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	accent   = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true)
	muted    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	good     = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	warn     = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))
	bad      = lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	selected = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Background(lipgloss.Color("236")).Bold(true)
	bold     = lipgloss.NewStyle().Bold(true)
)

type refreshMsg time.Time
type actionDone struct {
	state    *instanceState
	request  request
	instance string
	err      error
}
type uiAction struct {
	ID, Label, Kind, Target, Reason string
	Request                         request
	Page                            int
}
type detailRow struct {
	Text   string
	Action *uiAction
}
type tui struct {
	remote                                              *serviceClient
	ownedService                                        *embeddedService
	metadata                                            instanceState
	online                                              bool
	connectionErr                                       string
	pollLoop, networkRevision                           uint64
	missingBox, missingImage                            bool
	fleet                                               *fakeFleet
	host                                                *fakeHost
	state                                               fakeState
	instance                                            string
	width, height                                       int
	section, page, boxCursor, imageCursor, detailOffset int
	actionID, search, statusFilter, notice              string
	searching, help, pending, isError                   bool
	dialog                                              *form
	dialogMode                                          string
	editDraft                                           *form
	reviewRequest                                       request
	wizard                                              *creationWizard
	terminal                                            *tuiTerminal
}

func newTUI(host *fakeHost) *tui {
	return &tui{fleet: newFakeFleet(host), host: host, state: host.snapshot(), instance: "local-fake", width: 120, height: 36, statusFilter: "all"}
}
func refresh() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return refreshMsg(t) })
}
func (m *tui) Init() tea.Cmd {
	if m.remote != nil {
		return m.refreshService()
	}
	return refresh()
}
func (m *tui) boxes() []sandbox {
	result := []sandbox{}
	for _, b := range m.state.Sandboxes {
		if m.statusFilter != "all" && b.Status != m.statusFilter {
			continue
		}
		if strings.Contains(strings.ToLower(b.Name+" "+chainLabel(m.state.findImage(b.Image).Toolchains)), strings.ToLower(m.search)) {
			result = append(result, b)
		}
	}
	return result
}
func (m *tui) images() []image {
	out := []image{}
	for _, i := range m.state.Images {
		if strings.Contains(strings.ToLower(i.Name+" "+i.ID+" "+i.Digest), strings.ToLower(m.search)) {
			out = append(out, i)
		}
	}
	return out
}
func (m *tui) box() *sandbox {
	b := m.boxes()
	if len(b) == 0 || m.missingBox {
		return nil
	}
	m.boxCursor = min(max(0, m.boxCursor), len(b)-1)
	return &b[m.boxCursor]
}
func (m *tui) image() image {
	imgs := m.images()
	if len(imgs) == 0 || m.missingImage {
		return image{}
	}
	m.imageCursor = min(max(0, m.imageCursor), len(imgs)-1)
	return imgs[m.imageCursor]
}
func (m *tui) info() instanceInfo {
	if m.remote == nil {
		return m.fleet.snapshot(m.instance).Instance
	}
	for _, i := range m.metadata.Instances {
		if i.ID == m.instance {
			return i
		}
	}
	return instanceInfo{ID: m.instance, Name: "Connecting to backend"}
}
func (m *tui) message(text string, err bool) { m.notice, m.isError = text, err }
func (m *tui) clearFocus()                   { m.actionID = ""; m.detailOffset = 0 }
func (m *tui) switchInstance(id string) tea.Cmd {
	if m.remote != nil {
		m.pending = true
		m.networkRevision++
		m.pollLoop++
		return m.fetchService(id, true)
	}
	if m.fleet.hosts[id] == nil {
		return nil
	}
	m.instance = id
	m.host = m.fleet.hosts[id]
	m.state = m.host.snapshot()
	m.boxCursor, m.imageCursor = 0, 0
	m.search, m.statusFilter = "", "all"
	m.missingBox, m.missingImage = false, false
	m.clearFocus()
	m.message("Active instance: "+m.info().Name, false)
	return nil
}
func (m *tui) selectBox(name string) {
	m.missingBox = false
	m.search, m.statusFilter = "", "all"
	m.section, m.page = 0, 0
	for n, b := range m.boxes() {
		if b.Name == name {
			m.boxCursor = n
		}
	}
	m.clearFocus()
}
func (m *tui) selectImage(id string) {
	m.missingImage = false
	m.search = ""
	m.section = 1
	for n, i := range m.images() {
		if i.ID == id {
			m.imageCursor = n
		}
	}
	m.clearFocus()
}
func (m *tui) enabledActions() []uiAction {
	out := []uiAction{}
	for _, r := range m.detailRows() {
		if r.Action != nil && r.Action.Reason == "" {
			out = append(out, *r.Action)
		}
	}
	return out
}
func (m *tui) cycleAction(delta int) {
	list := m.enabledActions()
	if len(list) == 0 {
		m.actionID = ""
		return
	}
	idx := slices.IndexFunc(list, func(a uiAction) bool { return a.ID == m.actionID })
	if idx < 0 {
		idx = 0
		if delta < 0 {
			idx = len(list) - 1
		}
	} else {
		idx = (idx + delta + len(list)) % len(list)
	}
	m.actionID = list[idx].ID
	m.revealAction()
}
func (m *tui) revealAction() {
	rows := m.detailRows()
	available := max(2, m.height-14)
	for n, r := range rows {
		if r.Action != nil && r.Action.ID == m.actionID {
			if n < m.detailOffset {
				m.detailOffset = n
			}
			if n >= m.detailOffset+available {
				m.detailOffset = n - available + 1
			}
			return
		}
	}
}
func (m *tui) runSelected() tea.Cmd {
	if m.actionID == "" {
		m.cycleAction(1)
	}
	for _, a := range m.enabledActions() {
		if a.ID == m.actionID {
			return m.runAction(a)
		}
	}
	m.clearFocus()
	m.message("Choose an available action with Tab.", false)
	return nil
}
func (m *tui) runAction(a uiAction) tea.Cmd {
	if a.Reason != "" {
		m.message(a.Reason, true)
		return nil
	}
	switch a.Kind {
	case "image":
		m.selectImage(a.Target)
	case "sandbox":
		m.selectBox(a.Target)
	case "page":
		m.section, m.page = 0, a.Page
		m.clearFocus()
	case "instance":
		return m.switchInstance(a.Target)
	case "service":
		return m.toggleService(a.Target == "enable")
	case "attach":
		return m.attach(a.Request)
	case "form":
		m.open(a.Request.Action, a.Request.Name)
	default:
		return m.execute(a.Request)
	}
	return nil
}
func (m *tui) execute(r request) tea.Cmd {
	if m.remote != nil && !m.online {
		m.message("Backend offline. No command was sent or queued.", true)
		return nil
	}
	m.pending = true
	m.message("Working on "+m.info().Name+" …", false)
	host, id := m.host, m.instance
	if m.remote != nil {
		m.networkRevision++
		client := m.remote
		return func() tea.Msg {
			state, err := client.command(id, r)
			return actionDone{request: r, instance: id, err: err, state: &state}
		}
	}
	return func() tea.Msg { err := host.command(r); return actionDone{request: r, instance: id, err: err} }
}
func (m *tui) open(action, name string) {
	if action == "create" {
		m.startCreation()
		return
	}
	f := makeForm(action, name, m.state)
	m.dialog = &f
	m.dialogMode = "standard"
	m.clearFocus()
	m.message("", false)
	if action == "image-toolchains" {
		m.dialogMode = "image-edit"
		for n := range f.Fields {
			if f.Fields[n].Key == "approve" {
				f.Fields[n].Kind = "hidden"
			}
		}
	}
}
func (m *tui) instancePicker() {
	if len(m.instanceList()) == 0 {
		m.message("Connect to the backend before choosing an instance.", true)
		return
	}
	ids := []string{}
	for _, i := range m.instanceList() {
		ids = append(ids, i.ID)
	}
	m.dialog = &form{Action: "select-instance", Title: "Switch instance", Hint: "Each simulated instance owns its own sandboxes, images and sessions.", Fields: []field{{Key: "instance", Label: "Instance", Kind: "choice", Value: m.instance, Options: ids}}}
	m.dialogMode = "instance"
	m.message("", false)
}
func (m *tui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.revealAction()
	case serviceChanged:
		return m, m.receiveServiceChanged(msg)
	case serviceTick:
		if msg.loop != m.pollLoop {
			return m, nil
		}
		if m.pending {
			return m, m.pollService()
		}
		return m, m.fetchService(m.instance, false)
	case serviceSnapshot:
		return m, m.receiveSnapshot(msg)
	case serviceReview:
		return m, m.receiveReview(msg)
	case attachReady:
		return m, m.finishAttach(msg)
	case refreshMsg:
		if m.remote != nil {
			return m, m.refreshService()
		}
		m.state = m.host.snapshot()
		return m, refresh()
	case terminalOutput:
		return m, m.receiveTerminal(msg)
	case actionDone:
		m.pending = false
		if m.remote == nil {
			m.state = m.host.snapshot()
		} else if msg.err == nil && msg.state != nil {
			m.applySnapshot(*msg.state, false)
		}
		if msg.err != nil {
			m.connectionFailure(msg.err)
			return m, nil
		}
		if m.remote == nil {
			if msg.request.Action == "reset" || msg.request.Action == "scenario" {
				m.fleet.terminals.reset(msg.instance)
			} else {
				m.fleet.terminals.reconcile(msg.instance, m.fleet.hosts[msg.instance])
			}
		}
		if m.wizard != nil && msg.request.Action == "create" {
			m.wizard.Step = 4
			m.selectBox(msg.request.Name)
		}
		if msg.request.Action == "image-toolchains" || msg.request.Action == "build" {
			chains, _ := canonical(msg.request.Toolchains, toolchainCatalog)
			for _, img := range m.state.Images {
				if slices.Equal(img.Toolchains, chains) {
					m.selectImage(img.ID)
					break
				}
			}
		}
		if msg.request.Action == "reset" || msg.request.Action == "scenario" {
			m.boxCursor, m.imageCursor = 0, 0
			m.missingBox, m.missingImage = false, false
			m.search, m.statusFilter = "", "all"
			m.clearFocus()
		}
		m.dialog = nil
		m.editDraft = nil
		m.message("Action completed.", false)
		if len(m.state.Events) > 0 {
			m.notice = m.state.Events[0].Text
		}
	case tea.KeyMsg:
		key := msg.String()
		if m.terminal != nil {
			return m, m.terminalKey(msg)
		}
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.help {
			if key == "?" || key == "esc" || key == "q" {
				m.help = false
			} else if key == "pgdown" || key == "down" {
				m.detailOffset += 4
			} else if key == "pgup" || key == "up" {
				m.detailOffset = max(0, m.detailOffset-4)
			}
			return m, nil
		}
		if m.pending {
			return m, nil
		}
		if m.wizard != nil {
			return m, m.updateCreation(msg)
		}
		if m.dialog != nil {
			return m, m.updateDialog(msg)
		}
		if m.searching {
			switch key {
			case "esc":
				m.search = ""
				m.searching = false
			case "enter":
				m.searching = false
			case "ctrl+u":
				m.search = ""
			case "backspace":
				r := []rune(m.search)
				if len(r) > 0 {
					m.search = string(r[:len(r)-1])
				}
			default:
				if msg.Type == tea.KeyRunes {
					m.search += cleanInput(string(msg.Runes))
				}
			}
			m.boxCursor, m.imageCursor = 0, 0
			m.missingBox, m.missingImage = false, false
			m.clearFocus()
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "?":
			m.help = true
			m.detailOffset = 0
		case "1", "2", "3":
			m.section = int(key[0] - '1')
			m.search = ""
			m.clearFocus()
		case "left", ",", "right", ".":
			if m.section == 0 {
				delta := 1
				if key == "left" || key == "," {
					delta = -1
				}
				m.page = (m.page + delta + len(sandboxPages)) % len(sandboxPages)
				m.clearFocus()
			}
		case "R":
			if m.remote != nil {
				return m, m.refreshService()
			}
		case "up", "k", "down", "j":
			m.missingBox, m.missingImage = false, false
			delta := 1
			if key == "up" || key == "k" {
				delta = -1
			}
			if m.section == 0 {
				m.boxCursor = min(max(0, m.boxCursor+delta), max(0, len(m.boxes())-1))
			} else if m.section == 1 {
				m.imageCursor = min(max(0, m.imageCursor+delta), max(0, len(m.images())-1))
			} else {
				m.detailOffset = max(0, m.detailOffset+delta)
			}
			m.actionID = ""
			if m.section != 2 {
				m.detailOffset = 0
			}
		case "tab":
			m.cycleAction(1)
		case "shift+tab":
			m.cycleAction(-1)
		case "enter":
			return m, m.runSelected()
		case "pgdown":
			m.actionID = ""
			m.detailOffset += max(2, m.height/3)
		case "pgup":
			m.actionID = ""
			m.detailOffset = max(0, m.detailOffset-max(2, m.height/3))
		case "/":
			m.searching = true
		case "esc":
			m.search = ""
			m.boxCursor, m.imageCursor = 0, 0
			m.missingBox, m.missingImage = false, false
			m.clearFocus()
		case "v":
			m.statusFilter = map[string]string{"all": "running", "running": "stopped", "stopped": "all"}[m.statusFilter]
			m.boxCursor = 0
			m.clearFocus()
		case "g":
			m.instancePicker()
		case "n":
			m.startCreation()
		case "b":
			m.open("build", "")
		case "a", "i", "c", "e":
			if m.box() != nil {
				m.section = 0
				m.page = map[string]int{"a": 2, "i": 3, "c": 4, "e": 5}[key]
				m.clearFocus()
			}
		case "t":
			if m.section == 1 {
				if i := m.image(); i.ID != "" {
					m.open("image-toolchains", i.ID)
				}
			} else {
				m.section, m.page = 0, 1
				m.clearFocus()
			}
		case "s", "x", "d":
			if b := m.box(); b != nil {
				m.section, m.page = 0, 0
				id := map[string]string{"s": "start", "x": "stop", "d": "remove"}[key]
				if key == "d" {
					m.page = 4
				}
				m.clearFocus()
				for _, r := range m.detailRows() {
					if r.Action != nil && r.Action.Request.Action == id {
						return m, m.runAction(*r.Action)
					}
				}
				m.message("This action is unavailable for the selected sandbox.", true)
			}
		case "f":
			return m, m.execute(request{Action: "fault"})
		case "p":
			m.open("scenario", "")
		case "r":
			m.open("reset", "")
		}
	}
	return m, nil
}
func cleanInput(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
func fit(text string, width int) string { return ansi.Truncate(text, max(1, width), "…") }

func panel(title, content string, width, height int) string {
	inner := max(1, width-4)
	lines := []string{accent.Render(fit(title, inner))}
	for _, line := range strings.Split(content, "\n") {
		lines = append(lines, fit(line, inner))
	}
	lines = lines[:min(len(lines), max(1, height-2))]
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "239"}).Padding(0, 1).Width(max(1, width-2)).Height(max(1, height-2)).Render(strings.Join(lines, "\n"))
}
