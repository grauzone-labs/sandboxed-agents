package main

import "strings"

// These are the selected sandbox's data pages, never global catalogs.
var sandboxPages = []string{"Overview", "Image", "Agents", "Integrations", "Configuration", "Sessions"}

type sandboxAction struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
	Direct bool   `json:"direct"`
}

func sandboxActions(s fakeState, b sandbox) []sandboxAction {
	result := []sandboxAction{}
	if b.Sessions > 0 {
		result = append(result, sandboxAction{ID: "end-sessions", Label: "End sessions", Key: "e", Direct: true})
	}
	if b.Status == "running" {
		result = append(result, sandboxAction{ID: "stop", Label: "Stop", Key: "x", Direct: true})
	} else {
		result = append(result, sandboxAction{ID: "start", Label: "Start", Key: "s", Direct: true})
	}
	result = append(result,
		sandboxAction{ID: "update", Label: "Change toolchains", Key: "t"},
		sandboxAction{ID: "agents", Label: "Manage agents", Key: "a"},
		sandboxAction{ID: "integration", Label: "Connect integration", Key: "i"},
		sandboxAction{ID: "configure", Label: "Configure", Key: "c"},
		sandboxAction{ID: "remove", Label: "Delete", Key: "d"},
	)
	for idx := range result {
		a := &result[idx]
		if s.Busy[b.Name] != "" {
			a.Reason = "Action in progress …"
			continue
		}
		if b.Status != "running" && (a.ID == "agents" || a.ID == "integration" || a.ID == "end-sessions") {
			a.Reason = "Start the sandbox first"
		}
		if b.Sessions > 0 && (a.ID == "stop" || a.ID == "update" || a.ID == "agents" || a.ID == "configure" || a.ID == "remove") {
			a.Reason = "End agent sessions first"
		}
	}
	return result
}

func connectedIntegrations(b sandbox) string {
	connected := []string{}
	for _, key := range integrationCatalog {
		if value := b.Integrations[key]; value != "" && !strings.HasPrefix(value, "not") {
			connected = append(connected, key)
		}
	}
	if len(connected) == 0 {
		return "none connected"
	}
	return strings.Join(connected, ", ")
}

func sandboxActionReason(s fakeState, name, action string) string {
	b := s.findSandbox(name)
	if b == nil {
		return "No sandbox selected"
	}
	for _, a := range sandboxActions(s, *b) {
		if a.ID == action {
			return a.Reason
		}
	}
	return ""
}
