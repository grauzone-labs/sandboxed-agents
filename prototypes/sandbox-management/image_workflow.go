package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
)

type installedTool struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Toolchain string `json:"toolchain"`
}

type agentSession struct {
	ID       string `json:"id"`
	Agent    string `json:"agent"`
	Status   string `json:"status"`
	Started  string `json:"started"`
	Attached bool   `json:"attached"`
}

// Version numbers are illustrative fixtures, never probes of the local host.
func toolsForImage(img image) []installedTool {
	result := []installedTool{{"Git", "2.51.0", "base"}, {"Node.js", "22.19.0", "base"}, {"Python", "3.13.7", "base"}}
	fixtures := map[string][]installedTool{
		"azure":      {{"Azure CLI", "2.77.0", "azure"}},
		"dotnet":     {{".NET SDK", "10.0.100", "dotnet"}},
		"native":     {{"GCC", "14.2.0", "native"}, {"CMake", "3.31.6", "native"}},
		"playwright": {{"Playwright", "1.55.0", "playwright"}, {"Chromium", "140.0.7339.16", "playwright"}},
	}
	for _, chain := range img.Toolchains {
		result = append(result, fixtures[chain]...)
	}
	return result
}

func sessionsForSandbox(b sandbox) []agentSession {
	if len(b.OpenSessions) == b.Sessions && b.Sessions > 0 {
		return slices.Clone(b.OpenSessions)
	}
	result := []agentSession{}
	for n := 0; n < b.Sessions; n++ {
		agent := "unknown"
		if len(b.Agents) > 0 {
			agent = b.Agents[n%len(b.Agents)]
		}
		result = append(result, agentSession{ID: fmt.Sprintf("%s-session-%02d", b.Name, n+1), Agent: agent, Status: "running", Started: "2026-10-17 09:30 UTC (demo)"})
	}
	return result
}

// The token binds approval to the displayed image and all linked sandbox data.
// Unrelated sandbox changes do not invalidate the review.
func imageImpact(s fakeState, id string) string {
	linked := []sandbox{}
	for _, b := range s.Sandboxes {
		if b.Image == id {
			b.OpenSessions = sessionsForSandbox(b)
			linked = append(linked, b)
		}
	}
	slices.SortFunc(linked, func(a, b sandbox) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	data, _ := json.Marshal(struct {
		Image  image
		Linked []sandbox
	}{s.findImage(id), linked})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func applyImageToolchains(s *fakeState, r request, chains []string) (string, error) {
	img := s.findImage(r.Name)
	if img.ID == "" {
		return "", fmt.Errorf("Image no longer exists")
	}
	if r.Impact == "" || r.Impact != imageImpact(*s, r.Name) {
		return "", fmt.Errorf("Image or linked sandboxes changed. Cancel and review the impact again")
	}
	if !r.Approve {
		return "", fmt.Errorf("Confirm that all linked sandboxes will be updated and restarted, ending their open sessions")
	}
	if slices.Equal(img.Toolchains, chains) {
		return "", fmt.Errorf("Choose a different toolchain set before reviewing")
	}
	linked := s.imageUsers(r.Name)
	for _, name := range linked {
		if s.Busy[name] != "" {
			return "", fmt.Errorf("Sandbox %s has an action in progress; retry after it finishes", name)
		}
	}
	newID := s.imageFor(chains)
	for idx := range s.Sandboxes {
		b := &s.Sandboxes[idx]
		if b.Image != r.Name {
			continue
		}
		b.SessionSequence = max(b.SessionSequence, b.Sessions)
		b.Image, b.Status, b.Sessions, b.OpenSessions = newID, "running", 0, nil
		b.Restarts++
	}
	return fmt.Sprintf("Image toolchains configured · %d linked sandboxes updated and restarted · previous image kept", len(linked)), nil
}
