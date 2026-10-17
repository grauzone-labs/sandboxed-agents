package main

// THROWAWAY: Which TUI simplifies sandbox management?
// One Control room in the terminal and browser; run without flags for the TUI.
// web Control room: /?instance=local-fake|remote-lab. Not production code.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

var toolchainCatalog = []string{"azure", "dotnet", "native", "playwright"}
var agentCatalog = []string{"claude", "codex", "copilot", "opencode"}
var integrationCatalog = []string{"git", "github", "azure", "azdo"}
var sectionNames = []string{"Sandboxes", "Images", "Configuration"}
var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,47}$`)

type sandbox struct {
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	Image           string            `json:"image"`
	Agents          []string          `json:"agents"`
	Group           string            `json:"group"`
	Workspace       string            `json:"workspace"`
	CPU             int               `json:"cpu"`
	Memory          int               `json:"memory"`
	SSH             bool              `json:"ssh"`
	Sessions        int               `json:"sessions"`
	OpenSessions    []agentSession    `json:"openSessions"`
	SessionSequence int               `json:"sessionSequence"`
	Restarts        int               `json:"restarts"`
	Integrations    map[string]string `json:"integrations"`
	GitName         string            `json:"gitName"`
	GitEmail        string            `json:"gitEmail"`
}

type image struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Toolchains []string `json:"toolchains"`
	Size       string   `json:"size"`
	Created    string   `json:"created"`
	Digest     string   `json:"digest"`
	AssetHash  string   `json:"assetHash"`
	Platform   string   `json:"platform"`
}

type event struct {
	Time string `json:"time"`
	Text string `json:"text"`
}

type fakeState struct {
	ControllerGroup string                     `json:"controllerGroup"`
	AgentCatalog    []agentInfo                `json:"agentCatalog"`
	Actions         map[string][]sandboxAction `json:"actions,omitempty"`
	ImageTools      map[string][]installedTool `json:"imageTools"`
	Sandboxes       []sandbox                  `json:"sandboxes"`
	Retained        []sandbox                  `json:"retained"`
	Images          []image                    `json:"images"`
	Events          []event                    `json:"events"`
	Busy            map[string]string          `json:"busy"`
	FaultNext       bool                       `json:"faultNext"`
	Scenario        string                     `json:"scenario"`
}

// fakeHost is shared by both frontends. All effects end in this memory.
// It never invokes the production CLI, Podman, a shell, SSH or a login service.
type fakeHost struct {
	mu    sync.Mutex
	state fakeState
	delay time.Duration
	seed  func() fakeState
}

type request struct {
	ImageID     string   `json:"imageID"`
	ImageMode   string   `json:"imageMode"`
	Connect     []string `json:"connect"`
	Agent       string   `json:"agent"`
	Session     string   `json:"session"`
	Action      string   `json:"action"`
	Name        string   `json:"name"`
	Toolchains  []string `json:"toolchains"`
	Agents      []string `json:"agents"`
	Workspace   string   `json:"workspace"`
	Start       bool     `json:"start"`
	Confirm     string   `json:"confirm"`
	KeepVolumes bool     `json:"keepVolumes"`
	CPU         int      `json:"cpu"`
	Memory      int      `json:"memory"`
	SSH         bool     `json:"ssh"`
	GitName     string   `json:"gitName"`
	GitEmail    string   `json:"gitEmail"`
	Integration string   `json:"integration"`
	Scenario    string   `json:"scenario"`
	Approve     bool     `json:"approve"`
	Impact      string   `json:"impact"`
}

func seedState() fakeState {
	images := []image{
		{"img-base", "base", []string{}, "0.84 GB", "2026-10-17 08:40", "sha256:7e1c0a86b12f", "b38d90f7", "linux/amd64"},
		{"img-web", "native + playwright", []string{"native", "playwright"}, "2.41 GB", "2026-10-17 09:12", "sha256:2a8f93d41c70", "b38d90f7", "linux/amd64"},
		{"img-dotnet", "azure + dotnet", []string{"azure", "dotnet"}, "2.12 GB", "2026-10-16 14:32", "sha256:91bd03e65aa2", "b38d90f7", "linux/amd64"},
		{"img-native", "native", []string{"native"}, "1.28 GB", "2026-10-15 11:08", "sha256:45aa923e8120", "b38d90f7", "linux/amd64"},
	}
	box := func(name, status, imageID string, agents ...string) sandbox {
		return sandbox{Name: name, Status: status, Image: imageID, Agents: agents, Group: "default", Workspace: "volume:" + name + "-workspace", CPU: 4, Memory: 8,
			GitName: "Demo Developer", GitEmail: "demo@example.test", Integrations: map[string]string{"git": "configured", "github": "not connected", "azure": "not connected", "azdo": "not connected"}}
	}
	boxes := []sandbox{
		box("api-refactor", "running", "img-dotnet", "codex", "claude"),
		box("web-playground", "running", "img-web", "claude"),
		box("docs-agent", "running", "img-base", "copilot"),
		box("release-check", "stopped", "img-native", "codex"),
		box("experiment", "stopped", "img-base"),
	}
	boxes[0].Sessions, boxes[0].SSH = 1, true
	boxes[0].SessionSequence = 1
	boxes[0].Integrations["github"], boxes[0].Integrations["azure"] = "connected", "connected"
	boxes[1].Workspace, boxes[1].SSH = "/home/demo/projects/web", true
	return fakeState{ControllerGroup: "default", Sandboxes: boxes, Retained: []sandbox{}, Images: images, Busy: map[string]string{}, Events: []event{{"09:42:18", "Fake host ready · Controller group default"}}, Scenario: "normal"}
}

func newFakeHost(delay time.Duration) *fakeHost {
	return &fakeHost{state: seedState(), delay: delay, seed: seedState}
}

func cloneState(s fakeState) fakeState {
	data, _ := json.Marshal(s)
	var result fakeState
	_ = json.Unmarshal(data, &result)
	return result
}

func (f *fakeHost) snapshot() fakeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := cloneState(f.state)
	state.Actions = map[string][]sandboxAction{}
	state.AgentCatalog = fakeAgentCatalog()
	state.ImageTools = map[string][]installedTool{}
	for _, img := range state.Images {
		state.ImageTools[img.ID] = toolsForImage(img)
	}
	for idx := range state.Sandboxes {
		box := &state.Sandboxes[idx]
		box.OpenSessions = sessionsForSandbox(*box)
		state.Actions[box.Name] = sandboxActions(state, *box)
	}
	return state
}

func (s fakeState) findSandbox(name string) *sandbox {
	for i := range s.Sandboxes {
		if s.Sandboxes[i].Name == name {
			return &s.Sandboxes[i]
		}
	}
	return nil
}

func (s fakeState) findImage(id string) image {
	for _, i := range s.Images {
		if i.ID == id {
			return i
		}
	}
	return image{Toolchains: []string{}}
}

func (s fakeState) imageUsers(id string) []string {
	users := []string{}
	for _, b := range s.Sandboxes {
		if b.Image == id {
			users = append(users, b.Name)
		}
	}
	return users
}

func (f *fakeHost) log(text string) {
	f.state.Events = append([]event{{time.Now().Format("15:04:05"), text}}, f.state.Events...)
	if len(f.state.Events) > 40 {
		f.state.Events = f.state.Events[:40]
	}
}

func canonical(values, catalog []string) ([]string, error) {
	result := slices.Clone(values)
	slices.Sort(result)
	result = slices.Compact(result)
	for _, value := range result {
		if !slices.Contains(catalog, value) {
			return nil, fmt.Errorf("unknown catalog entry: %s", value)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result, nil
}

func chainLabel(chains []string) string {
	if len(chains) == 0 {
		return "none (Base image)"
	}
	return strings.Join(chains, ", ")
}

func (s *fakeState) imageFor(chains []string) string {
	for _, i := range s.Images {
		if slices.Equal(i.Toolchains, chains) {
			return i.ID
		}
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(chains, ","))))[:12]
	id := "img-fake-" + hash
	s.Images = append(s.Images, image{id, chainLabel(chains), chains, fmt.Sprintf("%.2f GB", .84+float64(len(chains))*.64), "just now", "sha256:fake" + hash, "b38d90f7", "linux/amd64"})
	return id
}

func (f *fakeHost) command(r request) error {
	f.mu.Lock()
	if r.Action == "reset" || r.Action == "scenario" {
		defer f.mu.Unlock()
		if len(f.state.Busy) > 0 {
			return fmt.Errorf("please wait for running fake actions to finish")
		}
		if r.Scenario != "" && !slices.Contains([]string{"normal", "empty", "many"}, r.Scenario) {
			return fmt.Errorf("unknown scenario")
		}
		f.state = f.seed()
		switch r.Scenario {
		case "empty":
			f.state.Sandboxes = []sandbox{}
		case "many":
			seedCount := len(f.state.Sandboxes)
			for n := 1; n <= 24; n++ {
				b := cloneState(f.state).Sandboxes[n%seedCount]
				b.Name, b.Sessions = fmt.Sprintf("feature-%02d", n), 0
				b.Workspace = "volume:" + b.Name + "-workspace"
				f.state.Sandboxes = append(f.state.Sandboxes, b)
			}
		}
		if r.Scenario != "" {
			f.state.Scenario = r.Scenario
		}
		f.log("Fake data reloaded")
		return nil
	}
	if r.Action == "fault" {
		f.state.FaultNext = !f.state.FaultNext
		f.mu.Unlock()
		return nil
	}
	r.Name = strings.TrimSpace(r.Name)
	key := r.Name
	if r.Action == "image-toolchains" {
		key = "@images"
	}
	if key == "" {
		key = "@images"
	}
	keys := []string{key}
	if r.Action == "image-toolchains" {
		keys = append(keys, f.state.imageUsers(r.Name)...)
	}
	if f.state.Busy["@images"] == "image-toolchains" && (r.Action == "create" || r.Action == "update") {
		f.mu.Unlock()
		return fmt.Errorf("Image update in progress; wait before linking a sandbox")
	}
	for _, lockKey := range keys {
		if f.state.Busy[lockKey] != "" {
			f.mu.Unlock()
			return fmt.Errorf("Lifecycle lock: an action is already running for %s", lockKey)
		}
	}
	// Validate and prepare a complete candidate. Failed actions never commit it.
	candidate := cloneState(f.state)
	message, err := applyRequest(&candidate, r)
	if err != nil {
		f.mu.Unlock()
		return err
	}
	fail := f.state.FaultNext
	f.state.FaultNext = false
	for _, lockKey := range keys {
		f.state.Busy[lockKey] = r.Action
	}
	f.log(r.Action + " · " + key + " · in progress …")
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, lockKey := range keys {
		delete(f.state.Busy, lockKey)
	}
	if fail {
		err := fmt.Errorf("simulated host error; changes discarded. Try again")
		f.log("ERROR · " + err.Error())
		return err
	}
	// Reapply under the lock: preserve unrelated concurrent sandbox mutations.
	current := cloneState(f.state)
	if _, err := applyRequest(&current, r); err != nil {
		f.log("ERROR · " + err.Error())
		return err
	}
	f.state = current
	f.log(message)
	return nil
}

func applyRequest(s *fakeState, r request) (string, error) {
	chains, err := canonical(r.Toolchains, toolchainCatalog)
	if err != nil {
		return "", err
	}
	agents, err := canonical(r.Agents, agentCatalog)
	if err != nil {
		return "", err
	}
	if r.Action == "create" {
		if !validName.MatchString(r.Name) {
			return "", fmt.Errorf("Name: 1–48 characters; letters, digits, dot, _ or -")
		}
		if s.findSandbox(r.Name) != nil {
			return "", fmt.Errorf("Sandbox name already taken")
		}
		if r.ImageMode != "" && len(agents) == 0 {
			return "", fmt.Errorf("Select at least one agent")
		}
		imageID := ""
		switch r.ImageMode {
		case "", "new":
			if r.ImageID != "" {
				return "", fmt.Errorf("Choose either an existing image or a new toolchain set")
			}
		case "existing":
			if r.ImageID == "" || s.findImage(r.ImageID).ID == "" {
				return "", fmt.Errorf("Select an existing image on this instance")
			}
			if len(chains) > 0 {
				return "", fmt.Errorf("Toolchains belong to the selected image; choose New image to change them")
			}
			imageID = r.ImageID
		default:
			return "", fmt.Errorf("Unknown image selection mode")
		}
		connections, err := canonical(r.Connect, []string{"github", "azure", "azdo"})
		if err != nil {
			return "", err
		}
		imageChains := chains
		if imageID != "" {
			imageChains = s.findImage(imageID).Toolchains
		}
		for _, connection := range connections {
			if (connection == "azure" || connection == "azdo") && !slices.Contains(imageChains, "azure") {
				return "", fmt.Errorf("Integration %s requires the azure toolchain in the image", connection)
			}
		}
		gitName, gitEmail := strings.TrimSpace(r.GitName), strings.TrimSpace(r.GitEmail)
		if gitName != "" || gitEmail != "" {
			if err := validateGitIdentity(gitName, gitEmail); err != nil {
				return "", err
			}
		}
		workspace := strings.TrimSpace(r.Workspace)
		if workspace == "" {
			workspace = "volume:" + r.Name + "-workspace"
		}
		status := "stopped"
		if r.Start {
			status = "running"
		}
		if imageID == "" {
			imageID = s.imageFor(chains)
		}
		b := sandbox{Name: r.Name, Status: status, Image: imageID, Agents: agents, Group: s.ControllerGroup, Workspace: workspace, CPU: 4, Memory: 8,
			Integrations: map[string]string{"git": "not configured", "github": "not connected", "azure": "not connected", "azdo": "not connected"}}
		for _, old := range s.Retained {
			if old.Name == r.Name {
				b.Agents, b.Integrations, b.GitName, b.GitEmail = old.Agents, old.Integrations, old.GitName, old.GitEmail
				if r.Workspace == "" {
					b.Workspace = old.Workspace
				}
			}
		}
		if r.ImageMode != "" {
			b.Agents = agents
		}
		if gitName != "" {
			b.GitName, b.GitEmail, b.Integrations["git"] = gitName, gitEmail, "configured"
		}
		for _, connection := range connections {
			b.Integrations[connection] = "connected (demo)"
		}
		s.Retained = slices.DeleteFunc(s.Retained, func(box sandbox) bool { return box.Name == r.Name })
		s.Sandboxes = append(s.Sandboxes, b)
		return r.Name + " created · " + status, nil
	}
	if r.Action == "build" {
		s.imageFor(chains)
		return "Image for " + chainLabel(chains) + " ready", nil
	}
	if r.Action == "image-toolchains" {
		return applyImageToolchains(s, r, chains)
	}
	b := s.findSandbox(r.Name)
	if b == nil {
		return "", fmt.Errorf("Sandbox %q does not exist", r.Name)
	}
	if slices.Contains([]string{"agent-enable", "agent-disable", "git-identity", "integration-connect", "integration-disconnect", "session-start", "session-end", "session-attach", "session-detach"}, r.Action) {
		return applySandboxItem(s, b, r)
	}
	if slices.Contains([]string{"stop", "remove", "update", "agents", "configure"}, r.Action) && b.Sessions > 0 {
		return "", fmt.Errorf("active agent session: end sessions with [e] first")
	}
	if slices.Contains([]string{"stop", "agents", "integration", "end-sessions"}, r.Action) && b.Status != "running" {
		return "", fmt.Errorf("Start the sandbox with [s] first")
	}
	switch r.Action {
	case "start":
		if b.Status == "running" {
			return "", fmt.Errorf("Sandbox is already running")
		}
		b.Status = "running"
	case "stop":
		b.Status = "stopped"
	case "end-sessions":
		b.SessionSequence = max(b.SessionSequence, b.Sessions)
		b.Sessions = 0
		b.OpenSessions = []agentSession{}
	case "remove":
		if r.Confirm != b.Name {
			return "", fmt.Errorf("to delete, type the exact sandbox name")
		}
		removed := *b
		s.Sandboxes = slices.DeleteFunc(s.Sandboxes, func(box sandbox) bool { return box.Name == r.Name })
		if r.KeepVolumes {
			s.Retained = append(s.Retained, removed)
			return r.Name + " deleted · workspace and home data kept (fake)", nil
		}
		return r.Name + " deleted with workspace and home data (fake)", nil
	case "update":
		b.Image = s.imageFor(chains)
	case "agents":
		b.Agents = agents
	case "configure":
		if r.CPU < 1 || r.CPU > 32 || r.Memory < 1 || r.Memory > 64 {
			return "", fmt.Errorf("CPU: 1–32; RAM: 1–64 GiB")
		}
		b.CPU, b.Memory, b.SSH, b.GitName, b.GitEmail = r.CPU, r.Memory, r.SSH, r.GitName, r.GitEmail
		if r.GitName != "" && r.GitEmail != "" {
			b.Integrations["git"] = "configured"
		}
	case "integration":
		if !slices.Contains(integrationCatalog, r.Integration) {
			return "", fmt.Errorf("unknown integration")
		}
		if slices.Contains([]string{"azure", "azdo"}, r.Integration) && !slices.Contains(s.findImage(b.Image).Toolchains, "azure") {
			return "", fmt.Errorf("Integration requires the azure toolchain. Update the image with [t] first")
		}
		if r.Integration == "git" {
			if b.GitName == "" || b.GitEmail == "" {
				return "", fmt.Errorf("Configure the git identity with [c] first")
			}
			b.Integrations["git"] = "configured"
		} else {
			b.Integrations[r.Integration] = "connected (demo)"
		}
	default:
		return "", fmt.Errorf("unknown fake action: %s", r.Action)
	}
	if r.Action == "update" {
		return r.Name + ": image updated · workspace and home data preserved", nil
	}
	return r.Name + " · " + r.Action + " completed", nil
}
