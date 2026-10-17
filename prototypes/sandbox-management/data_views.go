package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m *tui) itemReason(b sandbox, action, key string) string {
	if m.pending || m.state.Busy[b.Name] != "" {
		return "Action in progress"
	}
	if b.Status != "running" {
		return "Start the sandbox first"
	}
	if strings.HasPrefix(action, "agent-") && b.Sessions > 0 {
		return "End agent sessions before changing the agent selection"
	}
	if action == "integration-connect" && (key == "azure" || key == "azdo") && !slices.Contains(m.state.findImage(b.Image).Toolchains, "azure") {
		return "Requires the azure toolchain in the image"
	}
	return ""
}
func (m *tui) detailRows() []detailRow {
	rows := []detailRow{}
	text := func(s string) { rows = append(rows, detailRow{Text: s}) }
	pair := func(label, value string) { text(fmt.Sprintf("%-15s %s", label, value)) }
	action := func(a uiAction) {
		if a.ID == "" {
			a.ID = a.Kind + "/" + a.Request.Action + "/" + a.Target + "/" + a.Request.Agent + a.Request.Session + a.Request.Integration
		}
		rows = append(rows, detailRow{Action: &a})
	}
	tools := func(i image) {
		text("")
		text(muted.Render("INCLUDED TOOLS / illustrative versions"))
		for _, t := range toolsForImage(i) {
			text(fmt.Sprintf("%-15s %-12s %s", t.Name, t.Version, t.Toolchain))
		}
	}
	if m.section == 2 {
		text(bold.Render("Application configuration"))
		text(accent.Render(m.backendInfo()))
		if m.ownedService != nil {
			text("The TUI starts this local background service automatically.")
			pair("Web / API", m.ownedService.address())
			label, target := "Enable service", "enable"
			if m.ownedService.running() {
				label, target = "Disable service", "disable"
			}
			action(uiAction{Kind: "service", Target: target, Label: label})
			text("Disabling closes web access; local TUI data and sessions stay.")
			text("")
		}
		if m.remote != nil {
			if m.ownedService == nil {
				text("Availability is controlled by the hosting TUI or service process.")
			}
			pair("Backend", m.remote.address())
			pair("Service started", m.metadata.Service.Started)
			if m.ownedService == nil {
				text("Shared state survives closing clients; service restart resets it.")
			} else {
				text("This service runs with the hosting TUI. Disable/enable keeps state.")
			}
			text("")
		}
		text(muted.Render("Independent simulated hosts / use g to switch"))
		text("")
		for _, info := range m.instanceList() {
			label := info.Name
			if info.ID == m.instance {
				label += " / ACTIVE"
			}
			text(accent.Render(label))
			pair("Instance", info.ID)
			pair("Connection", info.Kind)
			pair("Address", info.Address)
			pair("Controller group", info.Group)
			pair("Inventory", fmt.Sprintf("%d sandboxes / %d images", info.SandboxCount, info.ImageCount))
			reason := ""
			if info.ID == m.instance {
				reason = "Current instance"
			}
			action(uiAction{Kind: "instance", Target: info.ID, Label: "Use this instance", Reason: reason})
			text("")
		}
		text(warn.Render("Remote authentication and adding instances are future work."))
		return rows
	}
	if m.section == 1 {
		i := m.image()
		if i.ID == "" {
			text("No matching images. Press Esc to clear search.")
			return rows
		}
		text(bold.Render(i.Name))
		pair("ID", i.ID)
		pair("Digest", i.Digest)
		pair("Asset hash", i.AssetHash)
		pair("Platform", i.Platform)
		pair("Created", i.Created)
		pair("Size", i.Size)
		pair("Toolchains", chainLabel(i.Toolchains))
		tools(i)
		text("")
		text(accent.Render("LINKED SANDBOXES"))
		users := m.state.imageUsers(i.ID)
		if len(users) == 0 {
			text("No linked sandboxes.")
		}
		for _, name := range users {
			b := m.state.findSandbox(name)
			action(uiAction{Kind: "sandbox", Target: name, Label: name + " / " + b.Status + " →"})
		}
		text("")
		text(warn.Render("Toolchain changes update and restart every linked sandbox."))
		action(uiAction{Kind: "form", Label: "Configure toolchains", Request: request{Action: "image-toolchains", Name: i.ID}})
		return rows
	}
	b := m.box()
	if b == nil {
		text("No sandbox selected.")
		text("n New sandbox · / Search · v Status · Esc clear")
		return rows
	}
	i := m.state.findImage(b.Image)
	text(bold.Render(b.Name) + " / " + b.Status)
	if m.width >= 110 {
		parts := []string{}
		for n, p := range sandboxPages {
			if n == m.page {
				parts = append(parts, accent.Render("["+p+"]"))
			} else {
				parts = append(parts, muted.Render(p))
			}
		}
		text(strings.Join(parts, " "))
	} else {
		text(accent.Render("‹ "+sandboxPages[m.page]+" ›") + muted.Render("  ←/→ tabs"))
	}
	text("")
	switch m.page {
	case 0:
		pair("Image", i.Name)
		pair("Toolchains", chainLabel(i.Toolchains))
		pair("Agents", strings.Join(b.Agents, ", "))
		pair("Workspace", b.Workspace)
		pair("Limits", fmt.Sprintf("%d CPU / %d GiB RAM", b.CPU, b.Memory))
		pair("SSH setup", fmt.Sprint(b.SSH))
		pair("Agent sessions", fmt.Sprint(b.Sessions))
		pair("Controller group", b.Group)
		pair("Image restarts", fmt.Sprint(b.Restarts))
		text("")
		for _, a := range sandboxActions(m.state, *b) {
			if a.ID == "start" || a.ID == "stop" {
				action(uiAction{Label: a.Label, Reason: a.Reason, Request: request{Action: a.ID, Name: b.Name}})
			}
		}
		if b.Sessions > 0 {
			action(uiAction{Kind: "page", Page: 5, ID: "view-sessions", Label: "View open sessions →"})
		}
	case 1:
		pair("Image", i.Name)
		pair("ID", i.ID)
		pair("Digest", i.Digest)
		pair("Asset hash", i.AssetHash)
		pair("Platform", i.Platform)
		pair("Created", i.Created)
		pair("Size", i.Size)
		pair("Toolchains", chainLabel(i.Toolchains))
		tools(i)
		text("")
		text(muted.Render("Toolchains belong to the shared image."))
		action(uiAction{Kind: "image", Target: i.ID, Label: "Open image / configure toolchains →"})
	case 2:
		text(muted.Render("ALL CATALOG AGENTS / illustrative versions"))
		text("")
		for _, a := range fakeAgentCatalog() {
			enabled := slices.Contains(b.Agents, a.ID)
			status, verb, cmd := "Disabled", "Enable", "agent-enable"
			if enabled {
				status, verb, cmd = "Enabled", "Disable", "agent-disable"
			}
			text(bold.Render(a.Name) + " / " + a.Version + " / " + status)
			action(uiAction{Label: verb + " " + a.ID, Reason: m.itemReason(*b, cmd, a.ID), Request: request{Action: cmd, Name: b.Name, Agent: a.ID}})
			text("")
		}
	case 3:
		text(bold.Render("Git identity"))
		pair("Git name", b.GitName)
		pair("Git email", b.GitEmail)
		action(uiAction{Kind: "form", Label: "Edit Git name and email", Reason: m.itemReason(*b, "git-identity", ""), Request: request{Action: "git-identity", Name: b.Name}})
		text("")
		for _, key := range []string{"github", "azure", "azdo"} {
			value := b.Integrations[key]
			connected := value != "" && !strings.HasPrefix(value, "not")
			verb, cmd := "Connect", "integration-connect"
			if connected {
				verb, cmd = "Disconnect", "integration-disconnect"
			}
			text(bold.Render(key) + " / " + value)
			action(uiAction{Label: verb + " " + key, Reason: m.itemReason(*b, cmd, key), Request: request{Action: cmd, Name: b.Name, Integration: key}})
			text("")
		}
		text(warn.Render("Connect workflow / to be defined"))
		text("Interactive sign-in requires SSH port forwarding.")
		text("A new workflow is needed; connections here are simulated.")
	case 4:
		pair("CPU limit", fmt.Sprint(b.CPU))
		pair("RAM limit", fmt.Sprintf("%d GiB", b.Memory))
		pair("SSH setup", fmt.Sprint(b.SSH))
		pair("Workspace", b.Workspace)
		pair("Controller group", b.Group)
		text("")
		for _, a := range sandboxActions(m.state, *b) {
			if a.ID == "configure" || a.ID == "remove" {
				action(uiAction{Kind: "form", Label: a.Label, Reason: a.Reason, Request: request{Action: a.ID, Name: b.Name}})
			}
		}
		if b.Sessions > 0 {
			action(uiAction{Kind: "page", Page: 5, ID: "config-sessions", Label: "View open sessions →"})
		}
	case 5:
		text(fmt.Sprintf("%d OPEN AGENT SESSIONS", b.Sessions))
		text("")
		agents := slices.Clone(b.Agents)
		for _, s := range sessionsForSandbox(*b) {
			if !slices.Contains(agents, s.Agent) {
				agents = append(agents, s.Agent)
			}
		}
		for _, id := range agents {
			version := "unknown"
			label := id
			for _, a := range fakeAgentCatalog() {
				if a.ID == id {
					version, label = a.Version, a.Name
				}
			}
			text(bold.Render(label) + " / " + version)
			open := false
			for _, s := range sessionsForSandbox(*b) {
				if s.Agent != id {
					continue
				}
				open = true
				pair("Session", s.ID)
				status := s.Status + " / detached"
				if s.Attached {
					status = s.Status + " / attached"
				}
				pair("Status", status)
				pair("Started", s.Started)
				action(uiAction{Kind: "attach", Label: "Attach / open terminal", Reason: m.itemReason(*b, "session-attach", s.ID), Request: request{Action: "session-attach", Name: b.Name, Session: s.ID}})
				action(uiAction{Label: "End session", Reason: m.itemReason(*b, "session-end", s.ID), Request: request{Action: "session-end", Name: b.Name, Session: s.ID}})
			}
			if !open {
				text(muted.Render("No open sessions for this agent."))
			}
			if slices.Contains(b.Agents, id) {
				action(uiAction{Label: "Start " + id + " session (demo)", Reason: m.itemReason(*b, "session-start", id), Request: request{Action: "session-start", Name: b.Name, Agent: id}})
			}
			text("")
		}
		if len(agents) == 0 {
			text("Enable an agent in the Agents tab first.")
		}
		text(muted.Render("Attach opens an interactive demo agent; Detach keeps history."))
	}
	return rows
}
func (m *tui) navigatorView(w, h int) string {
	lines := []string{muted.Render(m.info().Group + " / " + m.info().Name)}
	type entry struct {
		label  string
		chosen bool
	}
	entries := []entry{}
	entries = append(entries, entry{fmt.Sprintf("▾ Sandboxes (%d)", len(m.state.Sandboxes)), false})
	for n, b := range m.boxes() {
		symbol := "○"
		if b.Status == "running" {
			symbol = "●"
		}
		entries = append(entries, entry{"  " + symbol + " " + b.Name, m.section == 0 && n == m.boxCursor})
	}
	if len(m.boxes()) == 0 {
		entries = append(entries, entry{"  No matching sandboxes", false})
	}
	symbol := "▸"
	if m.section == 1 {
		symbol = "▾"
	}
	entries = append(entries, entry{fmt.Sprintf("%s Images (%d)", symbol, len(m.state.Images)), false})
	if m.section == 1 {
		for n, i := range m.images() {
			entries = append(entries, entry{"  " + i.Name, n == m.imageCursor})
		}
	}
	idx := 0
	for n, e := range entries {
		if e.chosen {
			idx = n
		}
	}
	count := max(1, h-5)
	start := min(max(0, idx-count+1), max(0, len(entries)-count))
	if start > 0 {
		lines = append(lines, muted.Render("↑ more objects"))
	} else {
		lines = append(lines, "")
	}
	for _, e := range entries[start:min(len(entries), start+count)] {
		line := fit(e.label, w-4)
		if e.chosen {
			line = selected.Render(fit("› "+strings.TrimSpace(e.label), w-4))
		}
		lines = append(lines, line)
	}
	if start+count < len(entries) {
		lines = append(lines, muted.Render("↓ more objects"))
	}
	return panel("01 / Navigation", strings.Join(lines, "\n"), w, h)
}
func (m *tui) detailsView(w, h int) string {
	rows := m.detailRows()
	wrapped := []string{}
	selectedLine := -1
	for _, r := range rows {
		line := r.Text
		if r.Action != nil {
			a := r.Action
			line = "  [ " + a.Label + " ]"
			if a.Reason != "" {
				line = muted.Render("  × " + a.Label + " / " + a.Reason)
			} else if a.ID == m.actionID {
				line = selected.Render("› [ " + a.Label + " ]")
				selectedLine = len(wrapped)
			} else {
				line = accent.Render(line)
			}
		}
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, max(1, w-5), " "), "\n")...)
	}
	count := max(1, h-4)
	if selectedLine >= 0 {
		if selectedLine < m.detailOffset {
			m.detailOffset = selectedLine
		}
		if selectedLine >= m.detailOffset+count {
			m.detailOffset = selectedLine - count + 1
		}
	}
	m.detailOffset = min(max(0, m.detailOffset), max(0, len(wrapped)-count))
	lines := wrapped[m.detailOffset:min(len(wrapped), m.detailOffset+count)]
	lines = append(slices.Clone(lines), muted.Render(fmt.Sprintf("PgUp/PgDn · %d–%d / %d", min(m.detailOffset+1, len(wrapped)), min(len(wrapped), m.detailOffset+count), len(wrapped))))
	title := "02 / Selected sandbox"
	if m.section == 1 {
		title = "02 / Image details"
	}
	if m.section == 2 {
		title = "Application configuration"
	}
	return panel(title, strings.Join(lines, "\n"), w, h)
}
func (m *tui) View() string {
	w, h := m.width, m.height
	if w < 60 || h < 20 {
		return "Sandboxed · Enlarge the terminal to at least 60 × 20.\nCtrl+C quits."
	}
	info := m.info()
	head := accent.Render("◈ SANDBOXED / Control room") + warn.Render("   FAKE / THROWAWAY")
	if m.remote != nil {
		if m.online {
			head += good.Render(" · Connected")
		} else {
			head += bad.Render(" · OFFLINE / reconnecting")
		}
	}
	running := 0
	for _, b := range m.state.Sandboxes {
		if b.Status == "running" {
			running++
		}
	}
	context := fmt.Sprintf("%s / %s / %s · %d running / %d stopped / %d images", info.Name, m.instance, info.Group, running, len(m.state.Sandboxes)-running, len(m.state.Images))
	tabs := []string{}
	for n, name := range sectionNames {
		label := fmt.Sprintf(" %d %s ", n+1, name)
		if n == m.section {
			label = selected.Render(label)
		} else {
			label = muted.Render(label)
		}
		tabs = append(tabs, label)
	}
	if m.wizard != nil {
		tabs = nil
		for n, name := range []string{"Image", "Sandbox", "Integrations", "Review"} {
			label := fmt.Sprintf(" %d %s ", n+1, name)
			if n == m.wizard.Step {
				label = selected.Render(label)
			} else if n < m.wizard.Step {
				label = good.Render(label)
			} else {
				label = muted.Render(label)
			}
			tabs = append(tabs, label)
		}
	}
	toolbar := "n New sandbox · b Build image · g Instance · / Search"
	if m.search != "" || m.searching {
		toolbar = "/ Search: " + m.search + "▏"
	}
	if m.section == 0 {
		toolbar += " · v " + m.statusFilter
	}
	if m.wizard != nil {
		toolbar = "Wizard · Esc Back · Ctrl+G Cancel"
		if m.wizard.Step == 0 {
			toolbar += " · F2 Image source"
		}
		if m.wizard.Step == 2 {
			toolbar += " · Ctrl+K Skip integrations"
		}
	}
	if m.dialog != nil {
		toolbar = "Editing on " + info.Name + " · Ctrl+S Next/Apply · Ctrl+G Cancel"
	}
	if m.terminal != nil {
		toolbar = "/help Commands · /status Session · Esc / Ctrl+D Detach"
	}
	contentH := h - 8
	var content string
	switch {
	case m.help:
		lines := strings.Split(ansi.Wrap(helpText, w-6, " "), "\n")
		count := max(1, contentH-4)
		m.detailOffset = min(max(0, m.detailOffset), max(0, len(lines)-count))
		visible := slices.Clone(lines[m.detailOffset:min(len(lines), m.detailOffset+count)])
		visible = append(visible, muted.Render("PgUp/PgDn Scroll help · ? / Esc Close"))
		content = panel("Help / Control room", strings.Join(visible, "\n"), w, contentH)
	case m.terminal != nil:
		content = m.terminalView(w, contentH)
	case m.wizard != nil:
		content = m.creationView(w, contentH)
	case m.dialog != nil:
		content = m.dialogView(w, contentH)
	default:
		if m.section == 2 {
			content = m.detailsView(w, contentH)
		} else {
			left := min(31, max(21, w/4))
			content = lipgloss.JoinHorizontal(lipgloss.Top, m.navigatorView(left, contentH), m.detailsView(w-left, contentH))
		}
	}
	status := m.notice
	if m.remote != nil && !m.online {
		status = "Backend offline / reconnecting · actions disabled · R Retry"
	}
	if status == "" {
		status = "All changes stay in memory. No real hosts are contacted."
	}
	if m.pending {
		status = "◌ " + status
	}
	if m.isError {
		status = bad.Render(status)
	} else {
		status = muted.Render(status)
	}
	footer := "↑↓ Objects · ←→ Tabs · Tab Action · Enter Run · PgUp/Dn Scroll · ? Help · q Quit"
	if m.dialog != nil || m.wizard != nil {
		footer = "Tab/↑↓ Fields · Space Toggle · Ctrl+S Next/Apply · Esc Back · Ctrl+G Cancel"
	}
	if m.terminal != nil {
		footer = "Interactive demo terminal · Esc / Ctrl+D Detach · Ctrl+C Interrupt · PgUp/Dn Scroll"
	}
	activity := ""
	if len(m.state.Events) > 0 {
		activity = m.state.Events[0].Time + " " + m.state.Events[0].Text
	}
	if m.state.FaultNext {
		activity = "ERROR ARMED / " + activity
	}
	lines := []string{fit(head, w), fit(muted.Render(context), w), fit(strings.Join(tabs, " "), w), fit(muted.Render(toolbar), w), content, fit(status, w), fit(muted.Render(activity), w), fit(accent.Render(footer), w)}
	out := strings.Split(strings.Join(lines, "\n"), "\n")
	for n := range out {
		out[n] = fit(out[n], w)
	}
	return strings.Join(out[:min(h, len(out))], "\n")
}

const helpText = `1 Sandboxes · 2 Images · 3 Application configuration
↑/↓ Choose object · ←/→ or ,/. Choose sandbox tab
Tab / Shift+Tab Choose an available action · Enter Run
PgUp/PgDn Scroll details · / Search · Esc Clear · v Status
n New sandbox wizard · b Build image · g Switch instance · R Refresh backend
a Agents · i Integrations · c Sandbox configuration · e Sessions
t Sandbox image / configure toolchains in Images
s Start · x Stop · d Delete (exact name confirmation)
f Fail next action · p Scenario · r Reset · q / Ctrl+C Quit

Forms: Tab fields and buttons · Space toggle · ←/→ choice
Ctrl+S Next/Apply · Esc Back · Ctrl+G Cancel
Wizard: F2 Existing/new image · Ctrl+K Skip integrations
Image changes require review and explicit restart approval.
Session terminal: type a request or /help, /status, /clear.
Esc / Ctrl+D detaches and keeps history; Ctrl+C interrupts.
All data and version numbers are simulated. ? / Esc closes help.`
