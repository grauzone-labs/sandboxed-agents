package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
)

// checkSandboxItems proves that the web detail actions mutate only the named
// agent, integration or session through the same HTTP boundary as the browser.
func checkSandboxItems(check func(bool, string) error) error {
	host := newFakeHost(0)
	handler := browserHandler(host)
	post := func(action, name string, values map[string]string) int {
		body, _ := json.Marshal(map[string]any{"action": action, "name": name, "values": values})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/command", strings.NewReader(string(body))))
		return rec.Code
	}
	before := host.snapshot()
	status := post("agent-disable", "api-refactor", map[string]string{"agent": "codex"})
	if err := check(status == 409 && reflect.DeepEqual(before, host.snapshot()), "Agent changes are blocked by open sessions without changing state"); err != nil {
		return err
	}
	status = post("agent-enable", "web-playground", map[string]string{"agent": "opencode"})
	after := host.snapshot()
	if err := check(status == 200 && slices.Equal(after.findSandbox("web-playground").Agents, []string{"claude", "opencode"}) && reflect.DeepEqual(before.findSandbox("api-refactor"), after.findSandbox("api-refactor")), "Enable targets one agent and one sandbox, preserving other selections"); err != nil {
		return err
	}
	status = post("agent-disable", "web-playground", map[string]string{"agent": "claude"})
	if err := check(status == 200 && slices.Equal(host.snapshot().findSandbox("web-playground").Agents, []string{"opencode"}), "Disable preserves the other enabled agent"); err != nil {
		return err
	}
	status = post("agent-disable", "web-playground", map[string]string{"agent": "claude"})
	unknown := post("agent-enable", "web-playground", map[string]string{"agent": "unknown"})
	stopped := post("agent-enable", "release-check", map[string]string{"agent": "claude"})
	if err := check(status == 409 && unknown == 409 && stopped == 409, "Unavailable, unknown and stopped-sandbox agent actions are rejected"); err != nil {
		return err
	}
	before = host.snapshot()
	status = post("git-identity", "api-refactor", map[string]string{"gitName": "New Developer", "gitEmail": "new@example.test"})
	git := host.snapshot().findSandbox("api-refactor")
	if err := check(status == 200 && git.GitName == "New Developer" && git.GitEmail == "new@example.test" && git.CPU == before.findSandbox("api-refactor").CPU && git.SSH == before.findSandbox("api-refactor").SSH && git.Sessions == 1, "Git identity editing changes only the selected sandbox identity while preserving resources and sessions"); err != nil {
		return err
	}
	before = host.snapshot()
	status = post("git-identity", "api-refactor", map[string]string{"gitName": "New Developer", "gitEmail": "invalid"})
	if err := check(status == 409 && reflect.DeepEqual(before, host.snapshot()), "Invalid Git identity leaves state untouched"); err != nil {
		return err
	}
	status = post("integration-disconnect", "api-refactor", map[string]string{"integration": "github"})
	if err := check(status == 200 && host.snapshot().findSandbox("api-refactor").Integrations["github"] == "not connected" && host.snapshot().findSandbox("api-refactor").Integrations["azure"] == "connected", "Disconnect affects only the selected integration"); err != nil {
		return err
	}
	status = post("integration-connect", "api-refactor", map[string]string{"integration": "github"})
	missingChain := post("integration-connect", "web-playground", map[string]string{"integration": "azure"})
	if err := check(status == 200 && host.snapshot().findSandbox("api-refactor").Integrations["github"] == "connected (demo)" && missingChain == 409, "Connect simulates one integration and enforces required image toolchains"); err != nil {
		return err
	}
	status = post("session-start", "api-refactor", map[string]string{"agent": "claude"})
	active := host.snapshot().findSandbox("api-refactor")
	first, second := active.OpenSessions[0].ID, active.OpenSessions[1].ID
	if err := check(status == 200 && active.Sessions == 2 && first != second && active.OpenSessions[1].Agent == "claude", "A demo session is started for the selected enabled agent with a distinct ID"); err != nil {
		return err
	}
	status = post("session-attach", "api-refactor", map[string]string{"session": second})
	active = host.snapshot().findSandbox("api-refactor")
	if err := check(status == 200 && !active.OpenSessions[0].Attached && active.OpenSessions[1].Attached, "Attach simulation targets only the selected session"); err != nil {
		return err
	}
	status = post("session-detach", "api-refactor", map[string]string{"session": second})
	if err := check(status == 200 && host.snapshot().findSandbox("api-refactor").Sessions == 2 && !host.snapshot().findSandbox("api-refactor").OpenSessions[1].Attached, "Detaching preserves running agent sessions"); err != nil {
		return err
	}
	status = post("session-end", "api-refactor", map[string]string{"session": first})
	active = host.snapshot().findSandbox("api-refactor")
	if err := check(status == 200 && active.Sessions == 1 && active.OpenSessions[0].ID == second && slices.Contains(active.Agents, "codex"), "End session preserves the other session's ID and the enabled agent"); err != nil {
		return err
	}
	wrongSandbox := post("session-end", "web-playground", map[string]string{"session": second})
	stale := post("session-end", "api-refactor", map[string]string{"session": first})
	disabledAgent := post("session-start", "api-refactor", map[string]string{"agent": "opencode"})
	if err := check(wrongSandbox == 409 && stale == 409 && disabledAgent == 409 && host.snapshot().findSandbox("api-refactor").Sessions == 1, "Session actions reject foreign or ended sessions and disabled agents"); err != nil {
		return err
	}
	post("session-start", "api-refactor", map[string]string{"agent": "codex"})
	active = host.snapshot().findSandbox("api-refactor")
	if err := check(active.OpenSessions[1].ID != first && active.OpenSessions[1].ID != second, "Session IDs remain stable and are not reused after ending a session"); err != nil {
		return err
	}
	post("end-sessions", "api-refactor", nil)
	status = post("agent-disable", "api-refactor", map[string]string{"agent": "codex"})
	if err := check(status == 200 && len(host.snapshot().findSandbox("api-refactor").OpenSessions) == 0 && slices.Equal(host.snapshot().findSandbox("api-refactor").Agents, []string{"claude"}), "Ending sessions unlocks agent changes and keeps session count consistent"); err != nil {
		return err
	}
	post("fault", "", nil)
	status = post("agent-enable", "api-refactor", map[string]string{"agent": "copilot"})
	if err := check(status == 409 && !slices.Contains(host.snapshot().findSandbox("api-refactor").Agents, "copilot") && len(host.snapshot().Busy) == 0, "Failed item action preserves selection and releases the sandbox lock"); err != nil {
		return err
	}
	return nil
}
