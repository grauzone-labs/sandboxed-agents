package main

import (
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
)

type agentInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// These versions are illustrative fixtures, not installed host programs.
func fakeAgentCatalog() []agentInfo {
	return []agentInfo{{"claude", "Claude Code", "2.1.12"}, {"codex", "Codex", "0.98.0"}, {"copilot", "GitHub Copilot", "0.0.395"}, {"opencode", "OpenCode", "1.1.25"}}
}

func validateGitIdentity(name, email string) error {
	parsed, err := mail.ParseAddress(email)
	if name == "" || err != nil || parsed.Address != email {
		return fmt.Errorf("Enter a Git name and a valid email address")
	}
	return nil
}

func applySandboxItem(s *fakeState, b *sandbox, r request) (string, error) {
	if b.Status != "running" {
		return "", fmt.Errorf("Start the sandbox first")
	}
	switch r.Action {
	case "agent-enable", "agent-disable", "session-start":
		if !slices.Contains(agentCatalog, r.Agent) {
			return "", fmt.Errorf("Unknown agent: %s", r.Agent)
		}
		enabled := slices.Contains(b.Agents, r.Agent)
		if r.Action == "session-start" {
			if !enabled {
				return "", fmt.Errorf("Enable this agent before starting a session")
			}
			b.OpenSessions = sessionsForSandbox(*b)
			b.SessionSequence = max(b.SessionSequence, b.Sessions) + 1
			b.OpenSessions = append(b.OpenSessions, agentSession{ID: fmt.Sprintf("%s-session-%02d", b.Name, b.SessionSequence), Agent: r.Agent, Status: "running", Started: time.Now().UTC().Format("2006-01-02 15:04 UTC") + " (demo)"})
			b.Sessions = len(b.OpenSessions)
			return b.Name + " · " + r.Agent + " session started (demo)", nil
		}
		if b.Sessions > 0 {
			return "", fmt.Errorf("End agent sessions before changing the agent selection")
		}
		if r.Action == "agent-enable" {
			if enabled {
				return "", fmt.Errorf("Agent is already enabled")
			}
			b.Agents = append(b.Agents, r.Agent)
			slices.Sort(b.Agents)
		} else {
			if !enabled {
				return "", fmt.Errorf("Agent is already disabled")
			}
			b.Agents = slices.DeleteFunc(b.Agents, func(a string) bool { return a == r.Agent })
		}
		return b.Name + " · " + r.Agent + " · " + r.Action + " completed", nil
	case "git-identity":
		name, email := strings.TrimSpace(r.GitName), strings.TrimSpace(r.GitEmail)
		if err := validateGitIdentity(name, email); err != nil {
			return "", err
		}
		b.GitName, b.GitEmail, b.Integrations["git"] = name, email, "configured"
		return b.Name + " · Git identity updated (demo)", nil
	case "integration-connect", "integration-disconnect":
		if !slices.Contains(integrationCatalog, r.Integration) || r.Integration == "git" {
			return "", fmt.Errorf("Choose an account integration")
		}
		if r.Action == "integration-connect" {
			if !strings.HasPrefix(b.Integrations[r.Integration], "not") {
				return "", fmt.Errorf("Integration is already connected")
			}
			if (r.Integration == "azure" || r.Integration == "azdo") && !slices.Contains(s.findImage(b.Image).Toolchains, "azure") {
				return "", fmt.Errorf("This integration requires the azure toolchain in the image")
			}
			b.Integrations[r.Integration] = "connected (demo)"
			return b.Name + " · " + r.Integration + " connected (demo; interactive login workflow pending)", nil
		}
		if strings.HasPrefix(b.Integrations[r.Integration], "not") {
			return "", fmt.Errorf("Integration is already disconnected")
		}
		b.Integrations[r.Integration] = "not connected"
		return b.Name + " · " + r.Integration + " disconnected (demo)", nil
	case "session-end", "session-attach", "session-detach":
		sessions := sessionsForSandbox(*b)
		idx := slices.IndexFunc(sessions, func(session agentSession) bool { return session.ID == r.Session })
		if idx < 0 {
			return "", fmt.Errorf("This session is no longer open in the selected sandbox")
		}
		if r.Action == "session-end" {
			b.SessionSequence = max(b.SessionSequence, b.Sessions)
			b.OpenSessions = slices.Delete(sessions, idx, idx+1)
			b.Sessions = len(b.OpenSessions)
		} else {
			attached := r.Action == "session-attach"
			if sessions[idx].Attached == attached {
				return "", fmt.Errorf("Session attachment state already changed; refresh and retry")
			}
			sessions[idx].Attached = attached
			b.OpenSessions = sessions
		}
		return b.Name + " · " + r.Session + " · " + r.Action + " completed (demo)", nil
	}
	return "", fmt.Errorf("Unknown sandbox item action")
}
