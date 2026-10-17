package main

import (
	"fmt"
	"slices"
	"strconv"
)

type field struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"`
	Value   string   `json:"value"`
	Options []string `json:"options,omitempty"`
}

type form struct {
	Action string  `json:"action"`
	Name   string  `json:"name"`
	Title  string  `json:"title"`
	Hint   string  `json:"hint"`
	Fields []field `json:"fields"`
	Focus  int     `json:"-"`
	Cursor int     `json:"-"`
}

func makeForm(action, name string, state fakeState) form {
	f := form{Action: action, Name: name, Fields: []field{}}
	text := func(key, label, value string) {
		f.Fields = append(f.Fields, field{Key: key, Label: label, Kind: "text", Value: value})
	}
	toggle := func(key, label string, value bool) {
		f.Fields = append(f.Fields, field{Key: key, Label: label, Kind: "bool", Value: strconv.FormatBool(value)})
	}
	choice := func(key, label, value string, options []string) {
		f.Fields = append(f.Fields, field{Key: key, Label: label, Kind: "choice", Value: value, Options: options})
	}
	b := state.findSandbox(name)
	if b == nil {
		b = &sandbox{CPU: 4, Memory: 8}
	}
	chains := func(chosen []string) {
		for _, c := range toolchainCatalog {
			toggle("chain:"+c, c, slices.Contains(chosen, c))
		}
	}
	agents := func(chosen []string) {
		for _, a := range agentCatalog {
			toggle("agent:"+a, a, slices.Contains(chosen, a))
		}
	}
	switch action {
	case "create-sandbox":
		f = makeForm("create", "", state)
		f.Action = action
		f.Fields = append(f.Fields, field{Key: "imageMode", Kind: "hidden"}, field{Key: "imageID", Kind: "hidden"})
		text("gitName", "Git name (optional)", "")
		text("gitEmail", "Git email (optional)", "")
		for _, integration := range []string{"github", "azure", "azdo"} {
			toggle("connect:"+integration, integration, false)
		}
	case "agent-enable", "agent-disable", "session-start":
		f.Title, f.Hint = "Agent action", "Simulated action for one agent in the selected sandbox."
		f.Fields = append(f.Fields, field{Key: "agent", Kind: "hidden"})
	case "session-end", "session-attach", "session-detach":
		f.Title, f.Hint = "Session action", "Simulated action for one agent session in the selected sandbox."
		f.Fields = append(f.Fields, field{Key: "session", Kind: "hidden"})
	case "integration-connect", "integration-disconnect":
		f.Title, f.Hint = "Integration action", "Connect or disconnect this integration in the fake only."
		f.Fields = append(f.Fields, field{Key: "integration", Kind: "hidden"})
	case "git-identity":
		f.Title, f.Hint = "Edit Git identity", "Update only the Git name and email of this sandbox."
		text("gitName", "Git name", b.GitName)
		text("gitEmail", "Git email", b.GitEmail)
	case "image-toolchains":
		f.Title, f.Hint = "Configure image toolchains", "Review the impact before applying. All linked sandboxes will switch image and restart; open agent sessions will end. Stopped sandboxes will start."
		chains(state.findImage(name).Toolchains)
		f.Fields = append(f.Fields, field{Key: "impact", Kind: "hidden", Value: imageImpact(state, name)})
		toggle("approve", "Update and restart all listed sandboxes, ending open sessions", false)
	case "create":
		f.Name = ""
		f.Title, f.Hint = "New sandbox", "An empty workspace field creates a dedicated volume. Everything stays in the fake."
		text("name", "Sandbox name", "")
		text("workspace", "Workspace (optional)", "")
		toggle("start", "Start after creating", true)
		chains(nil)
		agents([]string{"codex"})
	case "update", "build":
		f.Title, f.Hint = "Update toolchains / image", "The selection replaces the toolchain set. Workspace and home data are preserved."
		if action == "build" {
			f.Name = ""
			f.Title, f.Hint = "Build image", "Identical toolchain sets share one image. With no selection the base image is built."
		}
		chains(state.findImage(b.Image).Toolchains)
	case "agents":
		f.Title, f.Hint = "Manage agent selection", "Agents are enabled or disabled in the fake. The sandbox must be running with no active session."
		agents(b.Agents)
	case "integration":
		f.Title, f.Hint = "Connect integration", "Demo workflow: no real credentials. Azure and azdo require the azure toolchain."
		choice("integration", "Integration", "github", integrationCatalog)
	case "configure":
		f.Title, f.Hint = "Configure sandbox", "Resource changes are a UI draft. The existing CLI needs a re-create with preserved volumes for this."
		text("cpu", "CPU limit", strconv.Itoa(b.CPU))
		text("memory", "RAM limit (GiB)", strconv.Itoa(b.Memory))
		toggle("ssh", "SSH setup", b.SSH)
		text("gitName", "Git identity: name", b.GitName)
		text("gitEmail", "Git identity: email", b.GitEmail)
	case "remove":
		f.Title, f.Hint = "Delete sandbox", "To confirm, type the exact name: "+name+". The image is kept."
		text("confirm", "Confirm sandbox name", "")
		toggle("keepVolumes", "Keep workspace and home data", true)
	case "scenario":
		f.Name = ""
		f.Title, f.Hint = "Load fake scenario", "Discards all previous fake changes. No access to real sandboxes."
		choice("scenario", "Data set", state.Scenario, []string{"normal", "empty", "many"})
	case "stop":
		f.Title, f.Hint = "Stop sandbox", "The sandbox will be stopped. Workspace, home data and configuration are preserved."
	case "start":
		f.Title, f.Hint = "Start sandbox", "The existing image and the preserved data are used."
	case "end-sessions":
		f.Title, f.Hint = "End agent sessions", "All simulated agent sessions of this sandbox will be ended."
	case "reset":
		f.Name = ""
		f.Title, f.Hint = "Reset fake data", "The original data set replaces all previous fake changes."
	default:
		f.Title, f.Hint = "Unknown action", action
	}
	if len(f.Fields) > 0 {
		f.Cursor = len([]rune(f.Fields[0].Value))
	}
	return f
}

func (f form) request() (request, error) {
	values := map[string]string{}
	for _, field := range f.Fields {
		values[field.Key] = field.Value
	}
	r := request{Action: f.Action, Name: f.Name, Agent: values["agent"], Session: values["session"], Approve: values["approve"] == "true", Impact: values["impact"], Start: values["start"] == "true", Workspace: values["workspace"], Confirm: values["confirm"], KeepVolumes: values["keepVolumes"] == "true",
		SSH: values["ssh"] == "true", GitName: values["gitName"], GitEmail: values["gitEmail"], Integration: values["integration"], Scenario: values["scenario"]}
	if f.Action == "create" || f.Action == "create-sandbox" {
		r.Name = values["name"]
	}
	if f.Action == "create-sandbox" {
		r.Action, r.ImageMode, r.ImageID = "create", values["imageMode"], values["imageID"]
		if r.ImageMode != "existing" && r.ImageMode != "new" {
			return r, fmt.Errorf("Select an existing image or create a new image")
		}
	}
	for _, c := range toolchainCatalog {
		if values["chain:"+c] == "true" {
			r.Toolchains = append(r.Toolchains, c)
		}
	}
	for _, a := range agentCatalog {
		if values["agent:"+a] == "true" {
			r.Agents = append(r.Agents, a)
		}
	}
	if f.Action == "create-sandbox" {
		if len(r.Agents) == 0 {
			return r, fmt.Errorf("Select at least one agent")
		}
		for _, integration := range []string{"github", "azure", "azdo"} {
			if values["connect:"+integration] == "true" {
				r.Connect = append(r.Connect, integration)
			}
		}
	}
	if f.Action == "configure" {
		var err error
		r.CPU, err = strconv.Atoi(values["cpu"])
		if err != nil {
			return r, fmt.Errorf("CPU limit must be a whole number")
		}
		r.Memory, err = strconv.Atoi(values["memory"])
		if err != nil {
			return r, fmt.Errorf("RAM limit must be a whole number")
		}
	}
	return r, nil
}
