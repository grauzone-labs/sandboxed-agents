package main

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestTUITokenCreationIsCompleteAndReadOnly proves the entire one-time secret is visible at every minimum supported terminal size through Configuration keyboard/view seams.
func TestTUITokenCreationIsCompleteAndReadOnly(t *testing.T) {
	m := newTUI(newFakeHost(0))
	defer m.close()
	typeKeys(m, "3")
	m.openAdmin("token-create", "")
	setField(m.dialog, "name", "Narrow terminal")
	setField(m.dialog, "hours", "24")
	completeService(t, m, press(m, tea.KeyCtrlS))
	if m.dialog == nil || m.dialogMode != "secret" {
		t.Fatal("token creation must open a one-time display")
	}
	secret := fieldValue(m.dialog, "created-token")
	if len(secret) != 64 {
		t.Fatal("issued token must contain all 32 random bytes")
	}
	hexLine := regexp.MustCompile(`^[0-9a-f]{1,64}$`)
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 20}, {Width: 80, Height: 24}, {Width: 120, Height: 36}} {
		m.Update(size)
		view := ansi.Strip(m.View())
		var recovered strings.Builder
		for _, line := range strings.Split(view, "\n") {
			line = strings.Trim(line, " │")
			if hexLine.MatchString(line) {
				recovered.WriteString(line)
			}
		}
		if recovered.String() != secret {
			t.Fatalf("complete token must be readable without truncation at %dx%d", size.Width, size.Height)
		}
	}
	typeKeys(m, "attempt-edit")
	press(m, tea.KeyBackspace)
	if fieldValue(m.dialog, "created-token") != secret {
		t.Fatal("one-time token display must be read-only")
	}
	metadata := m.fleet.snapshot("local-fake")
	if len(metadata.Tokens) != 1 || metadata.Tokens[0].Name != "Narrow terminal" {
		t.Fatal("created token metadata must be listed")
	}
	press(m, tea.KeyEnter)
	if m.dialog != nil || strings.Contains(m.View(), secret) || strings.Contains(m.notice, secret) {
		t.Fatal("dismiss must remove the one-time token from the view and notices")
	}
	m.openAdmin("remote-connect", "")
	setField(m.dialog, "token", secret)
	if strings.Contains(m.View(), secret) || !strings.Contains(ansi.Strip(m.View()), "•") {
		t.Fatal("remote input must be masked")
	}
	press(m, tea.KeyCtrlG)
	if m.dialog != nil || strings.Contains(m.View(), secret) {
		t.Fatal("cancel must discard the masked draft")
	}
}

// TestHostingToggleWithRemoteSelection proves Disable/Enable retains A credentials and routes B inventory/actions through the TUI Configuration and real HTTP seams.
func TestHostingToggleWithRemoteSelection(t *testing.T) {
	b := authService(t)
	issued := issueTestToken(t, b.owner, "TUI connection", 3600)
	m, err := newHostedTUI(newFakeHost(0), freeServicePort(t), true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.openAdmin("remote-connect", "")
	setField(m.dialog, "label", "Actual B")
	setField(m.dialog, "url", b.server.URL)
	setField(m.dialog, "token", issued.Secret)
	cmd := press(m, tea.KeyCtrlS)
	if fieldValue(m.dialog, "token") != "" {
		t.Fatal("submission must clear the masked draft immediately")
	}
	next := completeService(t, m, cmd)
	completeService(t, m, next)
	if len(m.metadata.Instances) != 2 {
		t.Fatal("Configuration must register B")
	}
	id := m.metadata.Instances[1].ID
	completeService(t, m, m.switchInstance(id))
	if m.metadata.Origin == nil || m.metadata.Origin.ID != b.fleet.service.ID {
		t.Fatal("TUI must select actual B inventory")
	}
	aToken := issueTestToken(t, m.remote, "Retained A token", 3600)
	completeService(t, m, m.toggleService(false))
	if m.remote != nil || m.instance != id || m.host != m.fleet.hosts["local-fake"] || m.metadata.Origin.ID != b.fleet.service.ID {
		t.Fatal("Disable must retain the selected remote without treating it as a local host pointer")
	}
	completeService(t, m, m.execute(request{Action: "start", Name: "release-check"}))
	if snapshotTest(t, b.owner, "local-fake").findSandbox("release-check").Status != "running" || m.fleet.hosts["local-fake"].snapshot().findSandbox("release-check").Status != "stopped" {
		t.Fatal("sharing-off remote commands must reach B only")
	}
	completeService(t, m, m.toggleService(true))
	refreshClient(t, m)
	external, err := newServiceClient(m.ownedService.address(), aToken.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer external.close()
	if snapshotTest(t, external, id).Origin.ID != b.fleet.service.ID || m.instance != id {
		t.Fatal("Enable must preserve A tokens and selected remote")
	}
	revokeTestToken(t, b.owner, issued.Token.ID)
	refreshClient(t, m)
	if !m.online || m.environmentOnline() || m.info().Status != "disconnected" {
		t.Fatal("B token revocation must not mark A offline")
	}
	if m.execute(request{Action: "stop", Name: "release-check"}) != nil {
		t.Fatal("offline B commands must not be sent or queued")
	}
	typeKeys(m, "3")
	m.openAdmin("token-create", "")
	setField(m.dialog, "name", "A still available")
	completeService(t, m, press(m, tea.KeyCtrlS))
	press(m, tea.KeyEsc)
	if len(m.fleet.auth.list()) != 2 {
		t.Fatal("local token administration must remain usable while B is disconnected")
	}
	replacement := issueTestToken(t, b.owner, "TUI replacement", 3600)
	m.openAdmin("remote-reconnect", id)
	setField(m.dialog, "token", replacement.Secret)
	next = completeService(t, m, press(m, tea.KeyCtrlS))
	completeService(t, m, next)
	if !m.environmentOnline() || m.instance != id || m.state.findSandbox("release-check").Status != "running" {
		t.Fatal("replacement token must reconnect the same selection and retain B state")
	}
}
