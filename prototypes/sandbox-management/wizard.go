package main

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type creationWizard struct {
	Step                                    int
	Instance, Mode, ImageID                 string
	ImageCursor, ImageFocus, Button, Scroll int
	Draft                                   form
	Input                                   form
}

func (m *tui) startCreation() {
	w := &creationWizard{Instance: m.instance, Mode: "existing", Draft: makeForm("create-sandbox", "", m.state)}
	if len(m.state.Images) == 0 {
		w.Mode = "new"
	} else {
		w.ImageID = m.state.Images[0].ID
	}
	m.wizard = w
	m.creationInputs()
	m.message("Draft only. Nothing is created before the final confirmation.", false)
}
func (m *tui) syncCreation() {
	w := m.wizard
	for _, f := range w.Input.Fields {
		setField(&w.Draft, f.Key, f.Value)
	}
}
func (m *tui) creationInputs() {
	w := m.wizard
	w.Input = form{}
	w.Button, w.Scroll = 0, 0
	for _, f := range w.Draft.Fields {
		include := false
		switch w.Step {
		case 0:
			include = w.Mode == "new" && strings.HasPrefix(f.Key, "chain:")
		case 1:
			include = slices.Contains([]string{"name", "workspace", "start"}, f.Key) || strings.HasPrefix(f.Key, "agent:")
		case 2:
			include = slices.Contains([]string{"gitName", "gitEmail"}, f.Key) || strings.HasPrefix(f.Key, "connect:")
		}
		if !include {
			continue
		}
		if strings.HasPrefix(f.Key, "agent:") {
			for _, a := range fakeAgentCatalog() {
				if a.ID == strings.TrimPrefix(f.Key, "agent:") {
					f.Label = a.Name + " / " + a.Version
				}
			}
		}
		if strings.HasPrefix(f.Key, "connect:") {
			f.Label = "Connect " + map[string]string{"github": "GitHub", "azure": "Azure", "azdo": "Azure DevOps"}[strings.TrimPrefix(f.Key, "connect:")]
		}
		if w.Step == 2 && (f.Key == "connect:azure" || f.Key == "connect:azdo") && !slices.Contains(m.creationChains(), "azure") {
			setField(&w.Draft, f.Key, "false")
			continue
		}
		w.Input.Fields = append(w.Input.Fields, f)
	}
	if len(w.Input.Fields) > 0 {
		w.Input.Cursor = len([]rune(w.Input.Fields[0].Value))
	}
}
func (m *tui) creationChains() []string {
	w := m.wizard
	if w.Mode == "existing" {
		return m.state.findImage(w.ImageID).Toolchains
	}
	chains := []string{}
	for _, c := range toolchainCatalog {
		if fieldValue(&w.Draft, "chain:"+c) == "true" {
			chains = append(chains, c)
		}
	}
	return chains
}
func (m *tui) creationRequest(skip bool) (request, error) {
	w := m.wizard
	f := w.Draft
	f.Fields = slices.Clone(f.Fields)
	setField(&f, "imageMode", w.Mode)
	setField(&f, "imageID", "")
	if w.Mode == "existing" {
		setField(&f, "imageID", w.ImageID)
		for _, c := range toolchainCatalog {
			setField(&f, "chain:"+c, "false")
		}
	}
	for _, key := range []string{"name", "workspace", "gitName", "gitEmail"} {
		setField(&f, key, strings.TrimSpace(fieldValue(&f, key)))
	}
	if skip {
		setField(&f, "gitName", "")
		setField(&f, "gitEmail", "")
		for _, key := range []string{"github", "azure", "azdo"} {
			setField(&f, "connect:"+key, "false")
		}
	}
	return f.request()
}
func (m *tui) creationNext(skip bool) tea.Cmd {
	w := m.wizard
	m.syncCreation()
	if w.Instance != m.instance {
		m.message("Instance changed. Cancel and restart the wizard.", true)
		return nil
	}
	if w.Step == 4 {
		m.wizard = nil
		return nil
	}
	if w.Step == 0 {
		if w.Mode == "existing" && m.state.findImage(w.ImageID).ID == "" {
			m.message("Select an image on this instance.", true)
			return nil
		}
		w.Step = 1
		m.creationInputs()
		m.message("Choose at least one agent. Versions are illustrative.", false)
		return nil
	}
	if skip {
		for _, key := range []string{"gitName", "gitEmail"} {
			setField(&w.Draft, key, "")
		}
		for _, key := range []string{"github", "azure", "azdo"} {
			setField(&w.Draft, "connect:"+key, "false")
		}
	}
	r, err := m.creationRequest(w.Step == 1)
	if m.remote != nil {
		if err != nil {
			m.message(err.Error(), true)
			return nil
		}
		if w.Step == 3 {
			return m.execute(r)
		}
		return m.reviewService(r, "create")
	}
	if err == nil {
		candidate := cloneState(m.state)
		_, err = applyRequest(&candidate, r)
	}
	if err != nil {
		m.message(err.Error(), true)
		return nil
	}
	if w.Step == 3 {
		return m.execute(r)
	}
	w.Step++
	m.creationInputs()
	m.message("Draft only. No images or sandboxes have been created.", false)
	return nil
}
func (m *tui) creationBack() {
	m.syncCreation()
	w := m.wizard
	if w.Step == 0 {
		m.wizard = nil
		m.message("Cancelled. No changes made.", false)
		return
	}
	w.Step--
	m.creationInputs()
	m.message("Draft kept.", false)
}
func (m *tui) updateCreation(msg tea.KeyMsg) tea.Cmd {
	w := m.wizard
	key := msg.String()
	if key == "ctrl+g" {
		m.wizard = nil
		m.message("Cancelled. No changes made.", false)
		return nil
	}
	if key == "esc" {
		if w.Step == 4 {
			m.wizard = nil
		} else {
			m.creationBack()
		}
		return nil
	}
	if key == "pgdown" {
		w.Scroll += 4
		return nil
	}
	if key == "pgup" {
		w.Scroll = max(0, w.Scroll-4)
		return nil
	}
	if key == "f2" && w.Step == 0 {
		m.syncCreation()
		if w.Mode == "existing" {
			w.Mode = "new"
		} else if len(m.state.Images) > 0 {
			w.Mode = "existing"
		}
		m.creationInputs()
		return nil
	}
	if key == "ctrl+k" && w.Step == 2 {
		return m.creationNext(true)
	}
	if key == "ctrl+s" {
		return m.creationNext(false)
	}
	if w.Step == 4 {
		if key == "enter" {
			m.wizard = nil
		}
		return nil
	}
	if w.Step == 3 {
		if key == "tab" || key == "down" {
			w.Button = (w.Button + 1) % 3
		}
		if key == "shift+tab" || key == "up" {
			w.Button = (w.Button + 2) % 3
		}
		if key == "enter" {
			switch w.Button {
			case 0:
				return m.creationNext(false)
			case 1:
				m.creationBack()
			case 2:
				m.wizard = nil
				m.message("Cancelled. No changes made.", false)
			}
		}
		return nil
	}
	if w.Step == 0 && w.Mode == "existing" {
		if key == "tab" {
			w.ImageFocus = (w.ImageFocus + 1) % 3
		}
		if key == "shift+tab" {
			w.ImageFocus = (w.ImageFocus + 2) % 3
		}
		if key == "up" || key == "k" || key == "down" || key == "j" {
			delta := 1
			if key == "up" || key == "k" {
				delta = -1
			}
			w.ImageCursor = min(max(0, w.ImageCursor+delta), len(m.state.Images)-1)
			w.ImageID = m.state.Images[w.ImageCursor].ID
			w.Scroll = 0
			w.ImageFocus = 0
		}
		if key == "enter" {
			if w.ImageFocus == 2 {
				m.wizard = nil
				m.message("Cancelled. No changes made.", false)
				return nil
			}
			return m.creationNext(false)
		}
		return nil
	}
	n := len(visibleFields(&w.Input))
	buttons := 3
	if w.Step == 0 {
		buttons = 2
	}
	if w.Step == 2 {
		buttons = 4
	}
	if key == "enter" && w.Input.Focus >= n {
		b := w.Input.Focus - n
		if b == 0 {
			return m.creationNext(false)
		}
		if w.Step == 2 && b == 1 {
			return m.creationNext(true)
		}
		if (w.Step == 1 && b == 1) || (w.Step == 2 && b == 2) {
			m.creationBack()
			return nil
		}
		m.wizard = nil
		m.message("Cancelled. No changes made.", false)
		return nil
	}
	editFields(&w.Input, msg, buttons)
	m.syncCreation()
	return nil
}
func (m *tui) creationReview() []string {
	r, err := m.creationRequest(false)
	if err != nil {
		return []string{err.Error()}
	}
	candidate := cloneState(m.state)
	if _, err = applyRequest(&candidate, r); err != nil {
		return []string{err.Error()}
	}
	b := candidate.findSandbox(r.Name)
	i := candidate.findImage(b.Image)
	mode := "Use selected image"
	if r.ImageMode == "new" {
		mode = "Build a new image"
		if m.state.findImage(i.ID).ID != "" {
			mode = "Reuse matching image"
		}
	}
	git := b.GitName + " / " + b.GitEmail
	if b.GitName == "" {
		git = "Not configured"
	}
	connections := strings.Join(r.Connect, ", ")
	if connections == "" {
		connections = "Skipped / configure later"
	}
	lines := []string{bold.Render("Review new sandbox"), "Instance: " + m.info().Name + " / " + m.info().Group, "Name: " + b.Name, "Status after create: " + b.Status, "Image source: " + mode, "Image: " + i.Name + " / " + i.ID, "Workspace: " + b.Workspace, "Agents: " + strings.Join(b.Agents, ", "), "Git identity: " + git, "Connect integrations: " + connections, "", "Included tools / illustrative versions"}
	for _, t := range toolsForImage(i) {
		lines = append(lines, fmt.Sprintf("  %-16s %-12s %s", t.Name, t.Version, t.Toolchain))
	}
	for _, old := range m.state.Retained {
		if old.Name == r.Name {
			lines = append(lines, "", "Retained workspace and home data will be reused.", "Chosen agents replace the saved selection; saved integrations remain.")
		}
	}
	return lines
}
func (m *tui) creationView(w, h int) string {
	c := m.wizard
	stepNames := []string{"Image", "Sandbox", "Integrations (optional)", "Review", "Created"}
	title := fmt.Sprintf("New sandbox / %d · %s / %s", min(c.Step+1, 4), stepNames[c.Step], m.info().Name)
	if c.Step == 0 && c.Mode == "existing" {
		count := max(1, h-8)
		start := min(max(0, c.ImageCursor-count+1), max(0, len(m.state.Images)-count))
		left := []string{}
		for n := start; n < min(len(m.state.Images), start+count); n++ {
			i := m.state.Images[n]
			label := fit(i.Name, w/2-7)
			if n == c.ImageCursor {
				label = selected.Render("› " + label)
			} else {
				label = "  " + label
			}
			left = append(left, label)
		}
		left = append(left, muted.Render(fmt.Sprintf("↑↓ Images %d / %d", c.ImageCursor+1, len(m.state.Images))))
		i := m.state.findImage(c.ImageID)
		right := []string{bold.Render(i.Name), i.ID, i.Platform + " / " + i.Size, "", "Tool / illustrative version"}
		for _, t := range toolsForImage(i) {
			right = append(right, fmt.Sprintf("%-15s %s", t.Name, t.Version))
		}
		inner := h - 6
		previewRows := max(1, inner-4)
		c.Scroll = min(max(0, c.Scroll), max(0, len(right)-previewRows))
		right = append(slices.Clone(right[c.Scroll:min(len(right), c.Scroll+previewRows)]), muted.Render("PgUp/PgDn details"))
		content := lipgloss.JoinHorizontal(lipgloss.Top, panel("Available images", strings.Join(left, "\n"), w/2-2, inner), panel("Image details", strings.Join(right, "\n"), w-w/2-2, inner))
		buttons := []string{"Next →", "Cancel"}
		for n := range buttons {
			if c.ImageFocus == n+1 {
				buttons[n] = selected.Render("[ " + buttons[n] + " ]")
			} else {
				buttons[n] = accent.Render("[ " + buttons[n] + " ]")
			}
		}
		return panel(title, accent.Render("[Existing image]  New image · F2 Change source")+"\n"+content+"\n"+strings.Join(buttons, "  "), w, h)
	}
	if c.Step == 0 {
		return formPanel(&c.Input, title, "Existing image / [New image] · F2 Change source. Select toolchains; matching images are reused. No selection uses the base image.", w, h, []string{"Next →", "Cancel"}, nil, 0)
	}
	if c.Step == 1 {
		return formPanel(&c.Input, title, "Configure the sandbox and select at least one agent. An empty workspace creates a dedicated volume. Versions are illustrative.", w, h, []string{"Integrations →", "← Back", "Cancel"}, nil, 0)
	}
	if c.Step == 2 {
		hint := "Optional · Ctrl+K Skip · Git name + email together.\nLogin via SSH forwarding: workflow TBD / demo only."
		if !slices.Contains(m.creationChains(), "azure") {
			hint = "Azure / Azure DevOps require the azure image toolchain.\n" + hint
		}
		return formPanel(&c.Input, title, hint, w, h, []string{"Review →", "Skip integrations", "← Back", "Cancel"}, nil, 0)
	}
	lines := []string{}
	buttons := []string{}
	if c.Step == 3 {
		lines = m.creationReview()
		buttons = []string{"Create sandbox", "← Back", "Cancel"}
	} else {
		r, _ := m.creationRequest(false)
		b := m.state.findSandbox(r.Name)
		if b != nil {
			lines = []string{good.Render("✓ " + b.Name + " created"), "Status: " + b.Status, "Image: " + b.Image, "Agents: " + strings.Join(b.Agents, ", "), "Workspace: " + b.Workspace, "", "All changes remain in memory."}
		}
		buttons = []string{"Open sandbox"}
	}
	wrapped := []string{}
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(line, max(1, w-6), " "), "\n")...)
	}
	count := max(1, h-6)
	c.Scroll = min(max(0, c.Scroll), max(0, len(wrapped)-count))
	body := slices.Clone(wrapped[c.Scroll:min(len(wrapped), c.Scroll+count)])
	hint := "PgUp/PgDn Review · No changes until confirmed"
	if c.Step == 4 {
		hint = "Created in the fake instance · Enter opens the sandbox"
	}
	body = append(body, muted.Render(hint), "")
	label := "Create sandbox"
	if c.Step == 3 && c.Mode == "new" {
		chains := m.creationChains()
		found := false
		for _, i := range m.state.Images {
			if slices.Equal(i.Toolchains, chains) {
				found = true
			}
		}
		if !found {
			label = "Build image & create"
		}
	}
	if c.Step == 3 {
		buttons[0] = label
	}
	footer := buttonRows(buttons, c.Button, w-6)
	for len(body) < h-3-len(footer) {
		body = append(body, "")
	}
	body = append(body, footer...)
	return panel(title, strings.Join(body, "\n"), w, h)
}
