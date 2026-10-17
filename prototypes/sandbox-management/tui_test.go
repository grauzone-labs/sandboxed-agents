package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func press(m *tui, key tea.KeyType) tea.Cmd { _, cmd := m.Update(tea.KeyMsg{Type: key}); return cmd }
func typeKeys(m *tui, text string) tea.Cmd {
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	return cmd
}

// TestControlRoomNavigation proves that top-level configuration belongs to the application through the Bubble Tea keyboard/view seam.
func TestControlRoomNavigation(t *testing.T) {
	m := newTUI(newFakeHost(0))
	typeKeys(m, "3")
	if !strings.Contains(m.View(), "Application configuration") {
		t.Fatal("3 must open application configuration with remote instances")
	}
	if strings.Contains(m.View(), "Switch variant") {
		t.Fatal("retired variants must not appear")
	}
}

// TestNewSandboxWizard proves that keyboard creation waits for final confirmation and can skip integrations through Bubble Tea and the fake host.
func TestNewSandboxWizard(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	typeKeys(m, "n")
	if !strings.Contains(m.View(), "New sandbox / 1") {
		t.Fatal("n must open the new sandbox wizard")
	}
	press(m, tea.KeyCtrlS)
	typeKeys(m, "keyboard-box")
	press(m, tea.KeyCtrlS)
	if !strings.Contains(m.View(), "Integrations") {
		t.Fatal("integration step missing")
	}
	press(m, tea.KeyCtrlK)
	if host.snapshot().findSandbox("keyboard-box") != nil || len(host.snapshot().Images) != 4 {
		t.Fatal("review must not create objects")
	}
	if !strings.Contains(m.View(), "Review new sandbox") {
		t.Fatal("expected final review")
	}
	cmd := press(m, tea.KeyCtrlS)
	if cmd == nil {
		t.Fatal("final confirmation must execute")
	}
	m.Update(cmd())
	b := host.snapshot().findSandbox("keyboard-box")
	if b == nil || b.Image != "img-base" || len(b.Agents) != 1 || b.Agents[0] != "codex" || b.GitName != "" {
		t.Fatal("created sandbox must use the chosen image, codex and skipped integrations")
	}
}

// TestSessionTerminal proves attach, demo input, interrupt and history-preserving detach through the Bubble Tea seam and fake session state.
func TestSessionTerminal(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	typeKeys(m, "e")
	press(m, tea.KeyTab)
	cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("Attach must subscribe to the session terminal")
	}
	_, next := m.Update(cmd())
	if !host.snapshot().findSandbox("api-refactor").OpenSessions[0].Attached {
		t.Fatal("session should be attached")
	}
	typeKeys(m, "/status")
	_, next = m.Update(next())
	press(m, tea.KeyEnter)
	_, next = m.Update(next())
	if !strings.Contains(m.View(), "Instance: local-fake") {
		t.Fatal("terminal must transport input and demo output")
	}
	press(m, tea.KeyCtrlC)
	m.Update(next())
	press(m, tea.KeyEsc)
	if host.snapshot().findSandbox("api-refactor").OpenSessions[0].Attached {
		t.Fatal("Detach must clear attachment without ending the session")
	}
	cmd = press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("reattach must be available")
	}
	m.Update(cmd())
	if !strings.Contains(m.View(), "Instance: local-fake") {
		t.Fatal("reattach must keep terminal history")
	}
	press(m, tea.KeyEsc)
}

// TestImageChangeRequiresApproval proves image rollout approval and linked-session termination through Bubble Tea commands and the fake host.
func TestImageChangeRequiresApproval(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	typeKeys(m, "2")
	press(m, tea.KeyDown)
	press(m, tea.KeyDown)
	typeKeys(m, "t")
	press(m, tea.KeyTab)
	press(m, tea.KeyTab)
	typeKeys(m, " ") // add native to azure + dotnet
	if cmd := press(m, tea.KeyCtrlS); cmd != nil {
		t.Fatal("editing must enter review, not execute")
	}
	if !strings.Contains(m.View(), "api-refactor") || host.snapshot().findSandbox("api-refactor").Sessions != 1 {
		t.Fatal("review must list affected sandbox without ending sessions")
	}
	if cmd := press(m, tea.KeyCtrlS); cmd != nil {
		t.Fatal("approval is mandatory")
	}
	typeKeys(m, " ")
	cmd := press(m, tea.KeyCtrlS)
	if cmd == nil {
		t.Fatal("approved rollout must execute")
	}
	m.Update(cmd())
	b := host.snapshot().findSandbox("api-refactor")
	if b.Image == "img-dotnet" || b.Sessions != 0 || b.Restarts != 1 {
		t.Fatal("linked sandbox should use new image and have restarted without sessions")
	}
	if !strings.Contains(m.View(), b.Image) {
		t.Fatal("image details should follow the configured image")
	}
}

// TestRequiredAgentAndDraftBack proves required agent selection, preserved drafts and cancellation through wizard keystrokes.
func TestRequiredAgentAndDraftBack(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	typeKeys(m, "n")
	press(m, tea.KeyCtrlS)
	typeKeys(m, "agent-required")
	for range 4 {
		press(m, tea.KeyTab)
	}
	typeKeys(m, " ") // disable default Codex
	press(m, tea.KeyCtrlS)
	if !strings.Contains(m.View(), "Select at least one agent") {
		t.Fatal("wizard must reject an empty agent selection")
	}
	typeKeys(m, " ")
	press(m, tea.KeyCtrlS)
	typeKeys(m, "Draft User")
	press(m, tea.KeyTab)
	typeKeys(m, "draft@example.test")
	press(m, tea.KeyTab)
	typeKeys(m, " ")
	press(m, tea.KeyEsc)
	if !strings.Contains(m.View(), "agent-required") {
		t.Fatal("Back must preserve the sandbox name")
	}
	press(m, tea.KeyCtrlS)
	if !strings.Contains(m.View(), "draft@example.test") {
		t.Fatal("Back and Next must preserve integration drafts")
	}
	press(m, tea.KeyCtrlK)
	if strings.Contains(m.View(), "draft@example.test") {
		t.Fatal("Skip must clear optional draft integrations")
	}
	press(m, tea.KeyCtrlG)
	if len(host.snapshot().Sandboxes) != 5 || len(host.snapshot().Images) != 4 {
		t.Fatal("Cancel must leave inventory unchanged")
	}
}

// TestRemoteCreationAndInstanceIsolation proves that instance switching and creation operate only on the chosen host through keyboard commands.
func TestRemoteCreationAndInstanceIsolation(t *testing.T) {
	local := newFakeHost(0)
	m := newTUI(local)
	defer m.close()
	remoteFleet := newFakeFleet(newFakeHost(0))
	server := httptest.NewServer(remoteFleet.handler())
	defer server.Close()
	info, err := m.fleet.changeConnection(connectionRequest{Action: "connect", Label: "Second backend", URL: server.URL, Token: remoteFleet.ownerCredential})
	if err != nil {
		t.Fatal(err)
	}
	typeKeys(m, "g")
	press(m, tea.KeyRight)
	press(m, tea.KeyCtrlS)
	if m.instance != info.ID || m.metadata.Origin.ID != remoteFleet.service.ID {
		t.Fatal("must select the actual second backend")
	}
	typeKeys(m, "n")
	press(m, tea.KeyDown)
	press(m, tea.KeyCtrlS)
	typeKeys(m, "remote-created")
	completeService(t, m, press(m, tea.KeyCtrlS))
	completeService(t, m, press(m, tea.KeyCtrlK))
	completeService(t, m, press(m, tea.KeyCtrlS))
	press(m, tea.KeyEnter)
	if !strings.Contains(m.View(), "remote-created") || remoteFleet.hosts["local-fake"].snapshot().findSandbox("remote-created") == nil {
		t.Fatal("remote wizard must create on the second backend")
	}
	if len(local.snapshot().Sandboxes) != 5 || len(local.snapshot().Images) != 4 {
		t.Fatal("remote changes must leave local inventory untouched")
	}
	typeKeys(m, "g")
	press(m, tea.KeyLeft)
	press(m, tea.KeyCtrlS)
	if !strings.Contains(m.View(), "api-refactor") || strings.Contains(m.View(), "remote-created") {
		t.Fatal("switching back must restore local inventory")
	}
}

// TestNewImageFailureAndRetry proves that building a new image is atomic with creation and remains retryable through wizard commands.
func TestNewImageFailureAndRetry(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	cmd := typeKeys(m, "f")
	m.Update(cmd())
	typeKeys(m, "n")
	press(m, tea.KeyF2)
	press(m, tea.KeyTab)
	typeKeys(m, " ")
	press(m, tea.KeyTab)
	press(m, tea.KeyTab)
	typeKeys(m, " ") // dotnet + playwright
	press(m, tea.KeyCtrlS)
	typeKeys(m, "new-image-box")
	press(m, tea.KeyCtrlS)
	typeKeys(m, "Wizard User")
	press(m, tea.KeyTab)
	typeKeys(m, "wizard@example.test")
	press(m, tea.KeyTab)
	typeKeys(m, " ") // GitHub
	press(m, tea.KeyCtrlS)
	if len(host.snapshot().Images) != 4 {
		t.Fatal("review must not build an image")
	}
	cmd = press(m, tea.KeyCtrlS)
	if cmd == nil {
		t.Fatal("final create missing")
	}
	m.Update(cmd())
	if len(host.snapshot().Sandboxes) != 5 || len(host.snapshot().Images) != 4 || !strings.Contains(m.View(), "simulated host error") {
		t.Fatal("failed creation must leave no orphan image or sandbox")
	}
	cmd = press(m, tea.KeyCtrlS)
	if cmd == nil {
		t.Fatal("review should allow retry")
	}
	m.Update(cmd())
	b := host.snapshot().findSandbox("new-image-box")
	if b == nil || b.GitEmail != "wizard@example.test" || b.Integrations["github"] != "connected (demo)" || len(host.snapshot().Images) != 5 {
		t.Fatal("retry must create new image and configured sandbox")
	}
}

// TestScopedActions proves agent, integration and session actions run for their displayed sandbox and unavailable actions are skipped through Tab/Enter.
func TestScopedActions(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	typeKeys(m, "a")
	press(m, tea.KeyTab)
	if cmd := press(m, tea.KeyEnter); cmd != nil {
		t.Fatal("active sessions must block agent-selection changes")
	}
	press(m, tea.KeyDown) // web-playground has no session
	press(m, tea.KeyTab)
	cmd := press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("disable displayed Claude action should be available")
	}
	m.Update(cmd())
	b := host.snapshot().findSandbox("web-playground")
	if len(b.Agents) != 0 || len(host.snapshot().findSandbox("api-refactor").Agents) != 2 {
		t.Fatal("agent action must target only the selected sandbox")
	}
	press(m, tea.KeyTab)
	cmd = press(m, tea.KeyEnter)
	m.Update(cmd()) // enable Claude again
	typeKeys(m, "i")
	press(m, tea.KeyTab)
	press(m, tea.KeyEnter)
	press(m, tea.KeyCtrlU)
	typeKeys(m, "Terminal User")
	press(m, tea.KeyTab)
	press(m, tea.KeyCtrlU)
	typeKeys(m, "terminal@example.test")
	cmd = press(m, tea.KeyCtrlS)
	m.Update(cmd())
	if host.snapshot().findSandbox("web-playground").GitEmail != "terminal@example.test" {
		t.Fatal("Git identity editing must apply")
	}
	// First Tab selects Git edit; second selects GitHub Connect.
	press(m, tea.KeyTab)
	press(m, tea.KeyTab)
	cmd = press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("GitHub connect missing")
	}
	m.Update(cmd())
	if host.snapshot().findSandbox("web-playground").Integrations["github"] != "connected (demo)" {
		t.Fatal("integration action must connect GitHub")
	}
	typeKeys(m, "e")
	press(m, tea.KeyTab)
	cmd = press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("per-agent Start session missing")
	}
	m.Update(cmd())
	if host.snapshot().findSandbox("web-playground").Sessions != 1 {
		t.Fatal("session start must use the displayed enabled agent")
	}
	press(m, tea.KeyTab)
	press(m, tea.KeyTab)
	cmd = press(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("per-session End session missing")
	}
	m.Update(cmd())
	if host.snapshot().findSandbox("web-playground").Sessions != 0 {
		t.Fatal("session end must target its session")
	}
}

// TestLifecycleAndDeletion proves start, stop, sandbox configuration and exact-name deletion through the terminal keyboard seam.
func TestLifecycleAndDeletion(t *testing.T) {
	host := newFakeHost(0)
	m := newTUI(host)
	for range 3 {
		press(m, tea.KeyDown)
	} // stopped release-check
	cmd := typeKeys(m, "s")
	if cmd == nil {
		t.Fatal("Start missing")
	}
	m.Update(cmd())
	if host.snapshot().findSandbox("release-check").Status != "running" {
		t.Fatal("Start must target the selected sandbox")
	}
	typeKeys(m, "c")
	press(m, tea.KeyTab)
	press(m, tea.KeyEnter)
	press(m, tea.KeyCtrlU)
	typeKeys(m, "6")
	press(m, tea.KeyTab)
	press(m, tea.KeyCtrlU)
	typeKeys(m, "12")
	cmd = press(m, tea.KeyCtrlS)
	m.Update(cmd())
	if host.snapshot().findSandbox("release-check").CPU != 6 {
		t.Fatal("resource configuration not applied")
	}
	cmd = typeKeys(m, "x")
	m.Update(cmd())
	if host.snapshot().findSandbox("release-check").Status != "stopped" {
		t.Fatal("Stop missing")
	}
	typeKeys(m, "d")
	typeKeys(m, "wrong")
	cmd = press(m, tea.KeyCtrlS)
	if cmd == nil {
		t.Fatal("backend should validate the deletion confirmation")
	}
	m.Update(cmd())
	if host.snapshot().findSandbox("release-check") == nil {
		t.Fatal("wrong name must not delete")
	}
	press(m, tea.KeyCtrlU)
	typeKeys(m, "release-check")
	cmd = press(m, tea.KeyCtrlS)
	m.Update(cmd())
	if host.snapshot().findSandbox("release-check") != nil || len(host.snapshot().Retained) != 1 {
		t.Fatal("confirmed deletion should preserve workspace and home data by default")
	}
}

// TestTerminalLayoutsAndImagePreview proves size bounds and access to overflowing tool versions through Bubble Tea views and scrolling.
func TestTerminalLayoutsAndImagePreview(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {100, 30}, {140, 42}} {
		m := newTUI(newFakeHost(0))
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		check := func(label string) {
			t.Helper()
			v := m.View()
			if lipgloss.Width(v) > size[0] || lipgloss.Height(v) > size[1] {
				t.Fatalf("%s overflows %dx%d", label, size[0], size[1])
			}
		}
		for section := 1; section <= 3; section++ {
			typeKeys(m, fmt.Sprint(section))
			check("section")
		}
		typeKeys(m, "1")
		for range 6 {
			press(m, tea.KeyRight)
			check("sandbox tab")
		}
		typeKeys(m, "n")
		check("image picker")
		press(m, tea.KeyCtrlS)
		typeKeys(m, "layout-box")
		check("sandbox fields")
		press(m, tea.KeyCtrlS)
		check("integrations")
		press(m, tea.KeyCtrlK)
		check("review")
		press(m, tea.KeyCtrlG)
		typeKeys(m, "2")
		typeKeys(m, "t")
		typeKeys(m, " ")
		check("image edit")
		press(m, tea.KeyCtrlS)
		check("image impact")
		if !strings.Contains(m.View(), "Update and restart") {
			t.Fatal("approval checkbox must remain visible at small sizes")
		}
	}
	m := newTUI(newFakeHost(0))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	typeKeys(m, "n")
	press(m, tea.KeyPgDown)
	if !strings.Contains(m.View(), "Python") {
		t.Fatal("wizard image tool versions must be scrollable")
	}
}
