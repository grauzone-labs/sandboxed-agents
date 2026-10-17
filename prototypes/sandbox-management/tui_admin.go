package main

import (
	"fmt"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
)

const tokenScopeHint = "Full control, including tokens and remote connections. Memory only: tokens and connections are lost when this process exits. Disable/Enable sharing preserves them."

type adminDone struct {
	action string
	secret string
	err    error
}

func (m *tui) openAdmin(action, id string) {
	f := form{Action: action, Name: id, Hint: tokenScopeHint}
	switch action {
	case "token-create":
		f.Title = "Create token on CURRENT hosting backend"
		f.Fields = []field{{Key: "name", Label: "Token name", Kind: "text"}, {Key: "hours", Label: "Lifetime (hours, 1–8760)", Kind: "text", Value: "24"}}
	case "token-revoke":
		f.Title = "Revoke token / " + id
		f.Hint = "Revocation immediately disconnects clients and terminal streams using this token. " + tokenScopeHint
	case "remote-connect":
		f.Title = "Connect remote environment"
		f.Hint = "Create a token on the remote hosting system, then enter it here. HTTPS required except loopback HTTP. No recursive remote chaining."
		f.Fields = []field{{Key: "label", Label: "Connection label", Kind: "text"}, {Key: "url", Label: "Backend origin URL", Kind: "text", Value: "http://127.0.0.1:4318"}, {Key: "token", Label: "Remote token", Kind: "secret"}}
	case "remote-reconnect":
		f.Title = "Reconnect remote / " + id
		f.Hint = "Enter a replacement token created on the remote system. The saved label and URL are kept."
		f.Fields = []field{{Key: "token", Label: "Replacement remote token", Kind: "secret"}}
	case "remote-disconnect":
		f.Title = "Disconnect remote / " + id
		f.Hint = "Closes terminal attachments through this connection and clears its credential. The remote backend and agent sessions keep running. Metadata stays available for reconnect."
	default:
		return
	}
	m.dialog = &f
	m.dialogMode = "admin"
	m.clearFocus()
	m.message("", false)
}
func (m *tui) submitAdmin() tea.Cmd {
	f := m.dialog
	action, id := f.Action, f.Name
	name, hours := fieldValue(f, "name"), fieldValue(f, "hours")
	input := connectionRequest{ID: id, Label: fieldValue(f, "label"), URL: fieldValue(f, "url"), Token: fieldValue(f, "token")}
	seconds := 0
	if action == "token-create" {
		n, err := strconv.Atoi(hours)
		if err != nil || n < 1 || n > 8760 {
			m.message("Token lifetime must be 1–8760 whole hours.", true)
			return nil
		}
		seconds = n * 3600
	}
	switch action {
	case "remote-connect":
		input.Action = "connect"
	case "remote-reconnect":
		input.Action = "reconnect"
	case "remote-disconnect":
		input.Action = "disconnect"
	}
	client, fleet := m.remote, m.fleet
	m.pending = true
	m.networkRevision++
	// Drop the secret input immediately; summaries and notices never contain it.
	setField(f, "token", "")
	return func() tea.Msg {
		var err error
		secret := ""
		if action == "token-create" {
			if client != nil {
				var result struct {
					Secret string `json:"secret"`
				}
				err = client.jsonCall("/api/tokens", "local-fake", map[string]any{"action": "create", "name": name, "expiresInSeconds": seconds}, &result)
				secret = result.Secret
			} else {
				_, secret, err = fleet.auth.issue(name, seconds)
			}
		} else if action == "token-revoke" {
			if client != nil {
				err = client.jsonCall("/api/tokens", "local-fake", map[string]string{"action": "revoke", "id": id}, nil)
			} else {
				err = fleet.auth.revoke(id)
			}
		} else if client != nil {
			err = client.jsonCall("/api/connections", "local-fake", input, nil)
		} else {
			_, err = fleet.changeConnection(input)
		}
		return adminDone{action, secret, err}
	}
}
func (m *tui) receiveAdmin(msg adminDone) tea.Cmd {
	m.pending = false
	if msg.err != nil {
		m.message(msg.err.Error(), true)
		return nil
	}
	m.dialog = nil
	if msg.secret != "" {
		m.dialog = &form{Title: "New token / shown ONCE", Hint: "Copy this token now. Dismiss clears it; it cannot be retrieved again. " + tokenScopeHint, Fields: []field{{Key: "created-token", Kind: "readonly", Label: "Token", Value: msg.secret}}}
		m.dialogMode = "secret"
	} else {
		m.message(fmt.Sprintf("%s completed on the hosting backend.", msg.action), false)
	}
	if m.remote != nil {
		return m.refreshService()
	}
	m.applySnapshot(m.fleet.snapshot(m.instance), false)
	return nil
}
