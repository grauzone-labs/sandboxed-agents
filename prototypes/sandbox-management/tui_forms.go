package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"slices"
	"strings"
)

func visibleFields(f *form) []int {
	out := []int{}
	for n, i := range f.Fields {
		if i.Kind != "hidden" {
			out = append(out, n)
		}
	}
	return out
}
func fieldValue(f *form, key string) string {
	for _, i := range f.Fields {
		if i.Key == key {
			return i.Value
		}
	}
	return ""
}
func setField(f *form, key, value string) {
	for n := range f.Fields {
		if f.Fields[n].Key == key {
			f.Fields[n].Value = value
			return
		}
	}
}

// editFields preserves the existing form keyboard seam and keeps button focus separate from values.
func editFields(f *form, msg tea.KeyMsg, buttons int) {
	visible := visibleFields(f)
	n := len(visible)
	key := msg.String()
	if key == "tab" || key == "down" || key == "enter" {
		f.Focus = (f.Focus + 1) % (n + buttons)
		if f.Focus < n {
			f.Cursor = len([]rune(f.Fields[visible[f.Focus]].Value))
		}
		return
	}
	if key == "shift+tab" || key == "up" {
		f.Focus = (f.Focus + n + buttons - 1) % (n + buttons)
		if f.Focus < n {
			f.Cursor = len([]rune(f.Fields[visible[f.Focus]].Value))
		}
		return
	}
	if f.Focus >= n {
		return
	}
	item := &f.Fields[visible[f.Focus]]
	if item.Kind == "bool" {
		if key == " " || key == "left" || key == "right" {
			if item.Value == "true" {
				item.Value = "false"
			} else {
				item.Value = "true"
			}
		}
		return
	}
	if item.Kind == "choice" {
		if (key == "left" || key == "right" || key == " ") && len(item.Options) > 0 {
			delta := 1
			if key == "left" {
				delta = -1
			}
			idx := slices.Index(item.Options, item.Value)
			item.Value = item.Options[(idx+delta+len(item.Options))%len(item.Options)]
		}
		return
	}
	runes := []rune(item.Value)
	f.Cursor = min(max(f.Cursor, 0), len(runes))
	switch key {
	case "left":
		f.Cursor = max(0, f.Cursor-1)
	case "right":
		f.Cursor = min(len(runes), f.Cursor+1)
	case "home", "ctrl+a":
		f.Cursor = 0
	case "end", "ctrl+e":
		f.Cursor = len(runes)
	case "ctrl+u":
		runes, f.Cursor = nil, 0
	case "backspace", "ctrl+h":
		if f.Cursor > 0 {
			runes = append(runes[:f.Cursor-1], runes[f.Cursor:]...)
			f.Cursor--
		}
	case "delete":
		if f.Cursor < len(runes) {
			runes = append(runes[:f.Cursor], runes[f.Cursor+1:]...)
		}
	default:
		if msg.Type == tea.KeyRunes {
			// Strip control characters from pasted input before terminal rendering.
			clean := []rune(strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return -1
				}
				return r
			}, string(msg.Runes)))
			if len(runes)+len(clean) <= 240 {
				runes = append(slices.Clone(runes[:f.Cursor]), append(clean, runes[f.Cursor:]...)...)
				f.Cursor += len(clean)
			}
		}
	}
	item.Value = string(runes)
}
func (m *tui) updateDialog(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+g" {
		m.dialog = nil
		m.editDraft = nil
		m.message("Cancelled. No changes made.", false)
		return nil
	}
	if key == "esc" {
		if m.dialogMode == "image-review" {
			m.dialog = m.editDraft
			m.dialogMode = "image-edit"
		} else {
			m.dialog = nil
		}
		m.message("Draft cancelled or returned to editing.", false)
		return nil
	}
	if key == "pgdown" {
		m.detailOffset += 4
		return nil
	}
	if key == "pgup" {
		m.detailOffset = max(0, m.detailOffset-4)
		return nil
	}
	f := m.dialog
	n := len(visibleFields(f))
	if key == "ctrl+s" || key == "enter" && f.Focus >= n {
		if key == "enter" && f.Focus == n+1 {
			if m.dialogMode == "image-review" {
				m.dialog = m.editDraft
				m.dialogMode = "image-edit"
			} else {
				m.dialog = nil
			}
			return nil
		}
		return m.submitDialog()
	}
	editFields(f, msg, 2)
	return nil
}
func (m *tui) submitDialog() tea.Cmd {
	f := m.dialog
	if m.dialogMode == "instance" {
		id := fieldValue(f, "instance")
		m.dialog = nil
		return m.switchInstance(id)
	}
	if m.dialogMode == "image-review" {
		r := m.reviewRequest
		r.Approve = fieldValue(f, "approve") == "true"
		if !r.Approve {
			m.message("Confirm the update and restart of all linked sandboxes.", true)
			return nil
		}
		return m.execute(r)
	}
	r, err := f.request()
	if err != nil {
		m.message(err.Error(), true)
		return nil
	}
	if m.dialogMode == "image-edit" {
		if m.remote != nil {
			return m.reviewService(r, "image")
		}
		r.Approve = true
		candidate := cloneState(m.state)
		if _, err = applyRequest(&candidate, r); err != nil {
			m.message(err.Error(), true)
			return nil
		}
		m.enterImageReview(r)
		return nil
	}
	return m.execute(r)
}
func formRows(f *form, w int) []string {
	lines := []string{}
	for pos, n := range visibleFields(f) {
		i := f.Fields[n]
		value := i.Value
		if i.Kind == "bool" {
			value = "[ ]"
			if i.Value == "true" {
				value = "[x]"
			}
		}
		if i.Kind == "choice" {
			value = "‹ " + value + " ›"
		}
		if i.Kind == "text" && pos == f.Focus {
			r := []rune(value)
			cursor := min(max(0, f.Cursor), len(r))
			value = string(r[:cursor]) + "▏" + string(r[cursor:])
		}
		label := fmt.Sprintf("%-34s %s", i.Label, value)
		if w < 80 {
			label = i.Label + ": " + value
		}
		if pos == f.Focus {
			label = selected.Render("› " + label)
		} else {
			label = "  " + label
		}
		lines = append(lines, fit(label, w))
	}
	return lines
}
func buttonRows(labels []string, focus, width int) []string {
	rows := []string{}
	line := ""
	for n, label := range labels {
		label = fit("[ "+label+" ]", width)
		if n == focus {
			label = selected.Render(label)
		} else {
			label = accent.Render(label)
		}
		if ansi.StringWidth(line)+ansi.StringWidth(label)+2 > width && line != "" {
			rows = append(rows, line)
			line = ""
		}
		if line != "" {
			line += "  "
		}
		line += label
	}
	if line != "" {
		rows = append(rows, line)
	}
	return rows
}
func formPanel(f *form, title, hint string, w, h int, buttons []string, extra []string, offset int) string {
	top := strings.Split(ansi.Wrap(hint, max(1, w-6), " "), "\n")
	if len(top) > 3 {
		top = top[:3]
	}
	top = append(top, "")
	fields := formRows(f, w-6)
	footer := buttonRows(buttons, f.Focus-len(visibleFields(f)), w-6)
	count := max(1, h-len(top)-len(footer)-5)
	lines := slices.Clone(top)
	start, total := 0, len(fields)
	if len(extra) > 0 {
		// The approval stays visible while the impact list scrolls independently.
		slots := max(0, count-len(fields))
		start = min(max(0, offset), max(0, len(extra)-slots))
		total = len(extra)
		lines = append(lines, extra[start:min(len(extra), start+slots)]...)
		lines = append(lines, fields...)
		count = slots
	} else {
		focus := f.Focus
		if focus >= len(fields) {
			focus = max(0, len(fields)-1)
		}
		start = min(max(0, focus-count+1), max(0, len(fields)-count))
		lines = append(lines, fields[start:min(len(fields), start+count)]...)
	}
	if start > 0 || start+count < total {
		lines = append(lines, muted.Render(fmt.Sprintf("↑↓ / PgUp/PgDn · %d–%d / %d", start+1, min(total, start+count), total)))
	} else {
		lines = append(lines, "")
	}
	lines = append(lines, "")
	for len(lines) < h-3-len(footer) {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	return panel(title, strings.Join(lines, "\n"), w, h)
}
func (m *tui) dialogView(w, h int) string {
	f := m.dialog
	extra := []string{}
	hint := f.Hint
	buttons := []string{"Apply", "Cancel"}
	if m.dialogMode == "image-edit" {
		buttons[0] = "Review →"
	}
	if m.dialogMode == "image-review" {
		r := m.reviewRequest
		hint = "All linked sandboxes will switch image and restart, including stopped sandboxes. Open sessions will end. Previous image and data are kept."
		extra = append(extra, "Image: "+r.Name, "New toolchains: "+chainLabel(r.Toolchains), "")
		for _, name := range m.state.imageUsers(r.Name) {
			b := m.state.findSandbox(name)
			extra = append(extra, fmt.Sprintf("%s / %s / %d sessions → running", name, b.Status, b.Sessions))
		}
		if len(m.state.imageUsers(r.Name)) == 0 {
			extra = append(extra, "No linked sandboxes.")
		}
		extra = append(extra, "")
		buttons = []string{"Confirm update & restart", "← Back"}
	}
	if m.dialogMode == "instance" {
		hint += " Current: " + m.info().Name
	}
	return formPanel(f, f.Title, hint, w, h, buttons, extra, m.detailOffset)
}

func (m *tui) enterImageReview(r request) {
	r.Approve = false
	m.reviewRequest = r
	m.editDraft = m.dialog
	m.dialog = &form{Action: "confirm-image", Title: "Review image toolchain change", Fields: []field{{Key: "approve", Kind: "bool", Label: "Update and restart all linked sandboxes", Value: "false"}}}
	m.dialogMode = "image-review"
	m.detailOffset = 0
	m.message("Review only. No changes made yet.", false)
}
