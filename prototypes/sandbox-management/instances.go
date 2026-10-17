package main

import "time"

type instanceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Address      string `json:"address"`
	Group        string `json:"group"`
	SandboxCount int    `json:"sandboxCount"`
	ImageCount   int    `json:"imageCount"`
	SeedCount    int    `json:"seedCount"`
}

type fakeFleet struct {
	hosts     map[string]*fakeHost
	instances []instanceInfo
	terminals *terminalHub
	service   serviceInfo
}

type instanceState struct {
	fakeState
	Instance  instanceInfo   `json:"instance"`
	Instances []instanceInfo `json:"instances"`
	Service   serviceInfo    `json:"service"`
}

func newFakeFleet(local *fakeHost) *fakeFleet {
	remote := &fakeHost{state: seedRemoteState(), seed: seedRemoteState, delay: local.delay}
	return &fakeFleet{hosts: map[string]*fakeHost{"local-fake": local, "remote-lab": remote}, instances: []instanceInfo{
		{ID: "local-fake", Name: "Local workstation", Kind: "Local / simulated", Address: "This machine", Group: "default", SeedCount: 5},
		{ID: "remote-lab", Name: "Remote build lab", Kind: "Remote / simulated", Address: "agent@build-lab.example.test:22", Group: "remote-ci", SeedCount: 4},
	}, terminals: newTerminalHub(), service: newServiceInfo()}
}

func (f *fakeFleet) snapshot(id string) instanceState {
	all := make([]instanceInfo, 0, len(f.instances))
	var selected instanceInfo
	for _, info := range f.instances {
		s := f.hosts[info.ID].snapshot()
		info.SandboxCount, info.ImageCount = len(s.Sandboxes), len(s.Images)
		all = append(all, info)
		if info.ID == id {
			selected = info
		}
	}
	return instanceState{fakeState: f.hosts[id].snapshot(), Instance: selected, Instances: all, Service: f.service}
}

func seedRemoteState() fakeState {
	s := seedState()
	s.ControllerGroup = "remote-ci"
	s.Images = []image{
		{"remote-base", "base", []string{}, "0.91 GB", "2026-10-17 07:20", "sha256:remote8f10a4", "c7e8a291", "linux/amd64"},
		{"remote-dotnet", "azure + dotnet", []string{"azure", "dotnet"}, "2.36 GB", "2026-10-17 07:30", "sha256:remotea21e03", "c7e8a291", "linux/amd64"},
		{"remote-web", "playwright", []string{"playwright"}, "2.19 GB", "2026-10-17 08:10", "sha256:remotef32c91", "c7e8a291", "linux/amd64"},
		{"remote-native", "native", []string{"native"}, "1.41 GB", "2026-10-16 19:00", "sha256:remote62bfa1", "c7e8a291", "linux/amd64"},
	}
	boxes := []sandbox{s.Sandboxes[0], s.Sandboxes[1], s.Sandboxes[3], s.Sandboxes[4]}
	names := []string{"payments-api", "ui-regression", "batch-worker", "release-check"}
	ids := []string{"remote-dotnet", "remote-web", "remote-native", "remote-base"}
	for n := range boxes {
		b := &boxes[n]
		b.Name, b.Image, b.Group = names[n], ids[n], s.ControllerGroup
		b.Workspace = "/srv/workspaces/" + b.Name
		b.GitName, b.GitEmail = "CI Developer", "ci@example.test"
		b.CPU, b.Memory = 8, 16
		b.OpenSessions = nil
	}
	boxes[1].Sessions, boxes[1].SessionSequence = 1, 1
	boxes[1].Integrations["github"] = "connected"
	boxes[3].Agents = []string{"copilot"}
	s.Sandboxes = boxes
	s.Events = []event{{time.Now().Format("15:04:05"), "Remote build lab ready · simulated connection · Controller group remote-ci"}}
	return s
}
